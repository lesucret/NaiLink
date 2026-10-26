//go:build ignore

// Independent loopback SSH server for protocol interoperability tests.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"
)

func main() {
	key, err := os.ReadFile(os.Args[1])
	if err != nil { panic(err) }
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil { panic(err) }
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if c.User() == "fixture" && string(password) == "fixture" { return nil, nil }
		return nil, fmt.Errorf("authentication failed")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:"+os.Args[2])
	if err != nil { panic(err) }
	for {
		conn, err := listener.Accept()
		if err != nil { return }
		go func() {
			defer conn.Close()
			server, channels, requests, err := ssh.NewServerConn(conn, config)
			if err != nil { return }
			defer server.Close()
			go ssh.DiscardRequests(requests)
			for channel := range channels {
				var target struct { Host string; Port uint32; Origin string; OriginPort uint32 }
				if channel.ChannelType() != "direct-tcpip" || ssh.Unmarshal(channel.ExtraData(), &target) != nil || target.Host != "127.0.0.1" || strconv.Itoa(int(target.Port)) != os.Args[3] {
					channel.Reject(ssh.Prohibited, "fixture destination only"); continue
				}
				upstream, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
				if err != nil { channel.Reject(ssh.ConnectionFailed, "unavailable"); continue }
				stream, requests, err := channel.Accept()
				if err != nil { upstream.Close(); continue }
				go ssh.DiscardRequests(requests)
				go func() {
					defer stream.Close(); defer upstream.Close()
					go func() { io.Copy(upstream, stream); upstream.(*net.TCPConn).CloseWrite() }()
					io.Copy(stream, upstream)
				}()
			}
		}()
	}
}
