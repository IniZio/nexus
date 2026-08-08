package forwarder

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/inetaf/tcpproxy"
	log "github.com/sirupsen/logrus"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const linkLocalSubnet = "169.254.0.0/16"

// AddressFilter gates a single outbound flow by destination. addr is
// "host:port" for TCP/UDP and a bare host (literal IP) for ICMP. A non-nil
// error drops the flow. A nil AddressFilter is a no-op, leaving upstream
// forwarding behaviour unchanged.
type AddressFilter func(addr string) error

// maxInFlightConns bounds concurrent forwarder requests. An AddressFilter may
// block while a decision is pending (e.g. an interactive egress gate), and
// each pending request holds one in-flight slot; raise the ceiling above
// upstream's default of 10 so held connections can't starve allowed traffic.
const maxInFlightConns = 64

func TCP(s *stack.Stack, nat map[tcpip.Address]tcpip.Address, natLock *sync.Mutex, ec2MetadataAccess bool, filter AddressFilter, dialer func(ctx context.Context, network, addr string) (net.Conn, error)) *tcp.Forwarder {
	return tcp.NewForwarder(s, 0, maxInFlightConns, func(r *tcp.ForwarderRequest) {
		localAddress := r.ID().LocalAddress

		if (!ec2MetadataAccess) && linkLocal().Contains(localAddress) {
			r.Complete(true)
			return
		}

		natLock.Lock()
		if replaced, ok := nat[localAddress]; ok {
			localAddress = replaced
		}
		natLock.Unlock()

		addr := net.JoinHostPort(localAddress.String(), fmt.Sprint(r.ID().LocalPort))
		if filter != nil {
			if err := filter(addr); err != nil {
				log.Tracef("TCP filter rejected %s: %v", addr, err)
				r.Complete(true)
				return
			}
		}

		var outbound net.Conn
		var err error
		if dialer != nil {
			outbound, err = dialer(context.Background(), "tcp", addr)
		} else {
			outbound, err = net.Dial("tcp", addr)
		}
		if err != nil {
			log.Tracef("net.Dial() = %v", err)
			r.Complete(true)
			return
		}

		var wq waiter.Queue
		ep, tcpErr := r.CreateEndpoint(&wq)
		r.Complete(false)
		if tcpErr != nil {
			if _, ok := tcpErr.(*tcpip.ErrConnectionRefused); ok {
				// transient error
				log.Debugf("r.CreateEndpoint() = %v", tcpErr)
			} else {
				log.Errorf("r.CreateEndpoint() = %v", tcpErr)
			}
			return
		}

		remote := tcpproxy.DialProxy{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return outbound, nil
			},
		}
		remote.HandleConn(gonet.NewTCPConn(&wq, ep))
	})
}

func linkLocal() *tcpip.Subnet {
	_, parsedSubnet, _ := net.ParseCIDR(linkLocalSubnet) // CoreOS VM tries to connect to Amazon EC2 metadata service
	subnet, _ := tcpip.NewSubnet(tcpip.AddrFromSlice(parsedSubnet.IP), tcpip.MaskFromBytes(parsedSubnet.Mask))
	return &subnet
}
