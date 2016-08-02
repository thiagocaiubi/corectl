// Copyright (c) 2016 by António Meireles  <antonio.meireles@reformi.st>.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

//  adapted from github.com/kubernetes/minikube/pkg/localkube/dns.go
//

package server

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/TheNewNormal/corectl/components/host/session"
	backendetcd "github.com/skynetservices/skydns/backends/etcd"
	skymetrics "github.com/skynetservices/skydns/metrics"
	skydns "github.com/skynetservices/skydns/server"
)

var (
	RecursiveNameServers = []string{
		"8.8.8.8:53",
		"8.8.4.4:53",
	}
	LocalDomainName = "coreos.local"
)

type DNSServer struct {
	sky           runner
	dnsServerAddr *net.UDPAddr
	done          chan struct{}
}

func (d *ServerContext) NewDNSServer(root,
	serverAddress string, ns []string) (err error) {
	var (
		dnsAddress *net.UDPAddr
		skyConfig  = &skydns.Config{
			DnsAddr:     serverAddress,
			Domain:      root,
			Nameservers: ns,
			MinTtl:      30,
		}
	)
	if dnsAddress, err = net.ResolveUDPAddr("udp", serverAddress); err != nil {
		return
	}

	skydns.SetDefaults(skyConfig)

	backend := backendetcd.NewBackend(d.EtcdClient, context.Background(),
		&backendetcd.Config{
			Ttl:      skyConfig.Ttl,
			Priority: skyConfig.Priority,
		})
	skyServer := skydns.New(backend, skyConfig)

	// setup so prometheus doesn't run into nil
	skymetrics.Metrics()

	d.DNSServer = &DNSServer{
		sky:           skyServer,
		dnsServerAddr: dnsAddress,
	}
	// make host visible to the VMs by Name
	if err = d.DNSServer.addRecord("corectld",
		session.Caller.Network.Address); err != nil {
		return
	}
	// ...
	if err = d.DNSServer.addRecord("corectld.ns.dns",
		session.Caller.Network.Address); err != nil {
		return
	}
	d.DNSServer.Start()
	return
}

func (dns *DNSServer) Start() {
	if dns.done != nil {
		fmt.Fprint(os.Stderr, pad("DNS server already started"))
		return
	}

	dns.done = make(chan struct{})

	go until(dns.sky.Run, os.Stderr, "skydns", 1*time.Second, dns.done)

}

func (dns *DNSServer) Stop() {
	teardownService()

	// closing chan will prevent servers from restarting but will not kill
	// running server
	close(dns.done)

}

// runner starts a server returning an error if it stops.
type runner interface {
	Run() error
}

func teardownService() {
	Daemon.DNSServer.rmRecord("corectld", session.Caller.Network.Address)
}

func invertDomain(in string) (out string) {
	s := strings.Split(in, ".")
	for x := len(s) - 1; x >= 0; x-- {
		out += s[x] + "/"
	}
	out = strings.TrimSuffix(out, "/")
	return
}

func (d *DNSServer) addRecord(hostName string, ip string) (err error) {
	fqdn := fmt.Sprintf("%s.%s", hostName, LocalDomainName)
	path := fmt.Sprintf("/skydns/%s",
		strings.Replace(invertDomain(fqdn), ".", "/", -1))

	if _, err = Daemon.EtcdClient.Set(context.Background(), path,
		"{\"host\":\""+ip+"\", \"TTL\": 20 }", nil); err != nil {
		return
	}
	// reverse
	_, err = Daemon.EtcdClient.Set(context.Background(),
		"/skydns/arpa/in-addr/"+strings.Replace(ip, ".", "/", -1),
		"{\"host\":\""+fqdn+"\", \"TTL\": 20 }", nil)
	return
}

func (d *DNSServer) rmRecord(hostName string, ip string) (err error) {
	fqdn := fmt.Sprintf("%s.%s", hostName, LocalDomainName)
	path := fmt.Sprintf("/skydns/%s",
		strings.Replace(invertDomain(fqdn), ".", "/", -1))
	if _, err =
		Daemon.EtcdClient.Delete(context.Background(), path, nil); err != nil {
		return
	}
	// reverse
	_, err = Daemon.EtcdClient.Delete(context.Background(),
		"/skydns/arpa/in-addr/"+strings.Replace(ip, ".", "/", -1), nil)
	return
}

// helpers bellow loaned from kubernetes/minikube/blob/master/pkg/util/utils.go
// we don't want to consume them straight as recent changes there bring a
// XXL dep tail

// Until endlessly loops the provided function until a message is received on
// the done channel. The function will wait the duration provided in sleep
// between function calls. Errors will be sent on provider Writer.
func until(fn func() error, w io.Writer,
	name string, sleep time.Duration, done <-chan struct{}) {
	var exitErr error
	for {
		select {
		case <-done:
			return
		default:
			exitErr = fn()
			if exitErr == nil {
				fmt.Fprintf(w, pad("%s: Exited with no errors.\n"), name)
			} else {
				fmt.Fprintf(w, pad("%s: Exit with error: %v"), name, exitErr)
			}

			// wait provided duration before trying again
			time.Sleep(sleep)
		}
	}
}
func pad(str string) string {
	return fmt.Sprint("\n%s\n", str)
}
