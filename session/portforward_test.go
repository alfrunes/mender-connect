// Copyright 2021 Northern.tech AS
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//        http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package session

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mendersoftware/go-lib-micro/ws"
	wspf "github.com/mendersoftware/go-lib-micro/ws/portforward"
	"github.com/stretchr/testify/assert"
)

func getFreeTCPPort() int {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

func TestPortForwardHandler(t *testing.T) {
	// unkonwn message
	msg := &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:   ws.ProtoTypePortForward,
			MsgType: "dummy",
		},
	}
	w := new(testWriter)
	portForwardHandler(msg, w)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}
	rsp := w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, ws.MessageTypeError, rsp.Header.MsgType)
	assert.Equal(t, errPortForwardUnkonwnMessageType.Error(), string(rsp.Body))
	// new
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:   ws.ProtoTypePortForward,
			MsgType: wspf.MessageTypePortForwardNew,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c1",
				wspf.PropertyPortForwardProtocol:     protocolTCP,
				wspf.PropertyPortForwardRemoteHost:   "localhost",
				wspf.PropertyPortForwardRemotePort:   uint64(getFreeTCPPort()),
			},
		},
	}
	w = new(testWriter)
	portForwardHandler(msg, w)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}
	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, ws.MessageTypeError, rsp.Header.MsgType)
	assert.Contains(t, string(rsp.Body), "connect: connection refused")
	// stop
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:   ws.ProtoTypePortForward,
			MsgType: wspf.MessageTypePortForwardStop,
		},
	}
	w = new(testWriter)
	portForwardHandler(msg, w)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}
	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, ws.MessageTypeError, rsp.Header.MsgType)
	assert.Equal(t, errPortForwardUnkonwnConnection.Error(), string(rsp.Body))
	// forward
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:   ws.ProtoTypePortForward,
			MsgType: wspf.MessageTypePortForward,
		},
	}
	w = new(testWriter)
	portForwardHandler(msg, w)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}
	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, ws.MessageTypeError, rsp.Header.MsgType)
	assert.Equal(t, errPortForwardUnkonwnConnection.Error(), string(rsp.Body))
}

func TestPortForwardHandlerSuccessfulConnection(t *testing.T) {
	// mock echo TCP server
	tcpPort := getFreeTCPPort()
	go func(tcpPort int) {
		l, err := net.Listen(protocolTCP, fmt.Sprintf("localhost:%d", tcpPort))
		if err != nil {
			panic(err)
		}
		defer l.Close()
		for {
			conn, err := l.Accept()
			if err != nil {
				panic(err)
			}
			go func(conn net.Conn) {
				buf := make([]byte, 1024)
				for {
					n, err := conn.Read(buf)
					if err != nil && err == io.EOF {
						return
					} else if err != nil {
						panic(err)
					}
					response := strings.ToUpper(string(buf[:n]))
					_, err = conn.Write([]byte(response))
					if err != nil {
						panic(err)
					}
					if response == "STOP" {
						time.Sleep(50 * time.Millisecond)
						conn.Close()
						return
					}
				}
			}(conn)
		}
	}(tcpPort)

	sessionID := "session_id"

	// c1: new
	msg := &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     ws.ProtoTypePortForward,
			MsgType:   wspf.MessageTypePortForwardNew,
			SessionID: sessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c1",
				wspf.PropertyPortForwardProtocol:     protocolTCP,
				wspf.PropertyPortForwardRemoteHost:   "localhost",
				wspf.PropertyPortForwardRemotePort:   uint64(tcpPort),
			},
		},
	}
	w := new(testWriter)
	portForwardHandler(msg, w)

	time.Sleep(100 * time.Millisecond)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}

	rsp := w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForwardNew, rsp.Header.MsgType)
	assert.Nil(t, rsp.Body)

	// c1: forward
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     ws.ProtoTypePortForward,
			MsgType:   wspf.MessageTypePortForward,
			SessionID: sessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c1",
			},
		},
		Body: []byte("abcdefghi"),
	}
	w.Messages = []*ws.ProtoMsg{}
	portForwardHandler(msg, w)

	time.Sleep(100 * time.Millisecond)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}

	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForward, rsp.Header.MsgType)
	assert.Equal(t, "c1", rsp.Header.Properties[wspf.PropertyPortForwardConnectionID].(string))
	assert.Equal(t, []byte("ABCDEFGHI"), rsp.Body)

	// c2: new
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     ws.ProtoTypePortForward,
			MsgType:   wspf.MessageTypePortForwardNew,
			SessionID: sessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c2",
				wspf.PropertyPortForwardProtocol:     protocolTCP,
				wspf.PropertyPortForwardRemoteHost:   "localhost",
				wspf.PropertyPortForwardRemotePort:   uint64(tcpPort),
			},
		},
	}
	w.Messages = []*ws.ProtoMsg{}
	portForwardHandler(msg, w)

	time.Sleep(100 * time.Millisecond)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}

	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForwardNew, rsp.Header.MsgType)
	assert.Equal(t, "c2", rsp.Header.Properties[wspf.PropertyPortForwardConnectionID].(string))
	assert.Nil(t, rsp.Body)

	// c1: forward, again
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     ws.ProtoTypePortForward,
			MsgType:   wspf.MessageTypePortForward,
			SessionID: sessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c1",
			},
		},
		Body: []byte("1234"),
	}
	w.Messages = []*ws.ProtoMsg{}
	portForwardHandler(msg, w)

	time.Sleep(100 * time.Millisecond)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}

	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForward, rsp.Header.MsgType)
	assert.Equal(t, "c1", rsp.Header.Properties[wspf.PropertyPortForwardConnectionID].(string))
	assert.Equal(t, []byte("1234"), rsp.Body)

	// c1: stop
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     ws.ProtoTypePortForward,
			MsgType:   wspf.MessageTypePortForwardStop,
			SessionID: sessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c1",
			},
		},
	}
	w.Messages = []*ws.ProtoMsg{}
	portForwardHandler(msg, w)

	time.Sleep(100 * time.Millisecond)
	if !assert.Len(t, w.Messages, 1) {
		t.FailNow()
	}

	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForwardStop, rsp.Header.MsgType)
	assert.Equal(t, "c1", rsp.Header.Properties[wspf.PropertyPortForwardConnectionID].(string))
	assert.Nil(t, rsp.Body)

	// c2: forward the message with the "stop" payload
	msg = &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     ws.ProtoTypePortForward,
			MsgType:   wspf.MessageTypePortForward,
			SessionID: sessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: "c2",
			},
		},
		Body: []byte("stop"),
	}
	w.Messages = []*ws.ProtoMsg{}
	portForwardHandler(msg, w)

	time.Sleep(100 * time.Millisecond)
	if !assert.Len(t, w.Messages, 2) {
		t.FailNow()
	}

	rsp = w.Messages[0]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForward, rsp.Header.MsgType)
	assert.Equal(t, "c2", rsp.Header.Properties[wspf.PropertyPortForwardConnectionID].(string))
	assert.Equal(t, []byte("STOP"), rsp.Body)

	rsp = w.Messages[1]
	assert.Equal(t, ws.ProtoTypePortForward, rsp.Header.Proto)
	assert.Equal(t, wspf.MessageTypePortForwardStop, rsp.Header.MsgType)
	assert.Equal(t, "c2", rsp.Header.Properties[wspf.PropertyPortForwardConnectionID].(string))
	assert.Nil(t, rsp.Body)
}
