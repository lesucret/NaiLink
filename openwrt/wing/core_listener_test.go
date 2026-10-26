// SPDX-License-Identifier: AGPL-3.0-only
package control

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestRouterCoreListenerGenerationCancellation(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := &Listener{tcp4Listener: tcp, packetConn: udp}
	defer base.Close()
	ctx, cancel := context.WithCancel(context.Background())
	old, release, err := routerOwnServingListener(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 2)
	go func() {
		conn, e := old.tcp4Listener.Accept()
		if conn != nil {
			conn.Close()
		}
		done <- e
	}()
	go func() { _, _, e := old.packetConn.ReadFrom(make([]byte, 32)); done <- e }()
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			if e == nil {
				t.Fatal("old generation consumed a new request")
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled listener remained blocked")
		}
	}
	next, cleanup, err := routerOwnServingListener(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	go func() {
		conn, e := next.tcp4Listener.Accept()
		if conn != nil {
			conn.Close()
		}
		done <- e
	}()
	client, err := net.DialTimeout("tcp4", base.tcp4Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("first connection lost")
	}
	sender, err := net.Dial("udp4", base.packetConn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	_, _ = sender.Write([]byte("first DNS request"))
	_ = next.packetConn.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 64)
	n, _, err := next.packetConn.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "first DNS request" {
		t.Fatal("first datagram lost", err)
	}
}
