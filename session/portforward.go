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
	"net"
	"time"

	"github.com/mendersoftware/go-lib-micro/ws"
	wspf "github.com/mendersoftware/go-lib-micro/ws/portforward"
	"github.com/pkg/errors"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	portForwardBuffSize          = 4096
	portForwardConnectionTimeout = time.Second * 600
)

var (
	errPortForwardUnkonwnMessageType = errors.New("unknown message type")
)

type MenderPortForwarder struct {
	conn   net.Conn
	closed chan struct{}
}

func PortForward() Constructor {
	return func() SessionHandler {
		return &MenderPortForwarder{
			closed: make(chan struct{}),
		}
	}
}

func (pf *MenderPortForwarder) ServeProtoMsg(msg *ws.ProtoMsg, w ResponseWriter) {
	var err error
	switch msg.Header.MsgType {
	case wspf.MessageTypePortForwardNew:
		err = pf.HandleNewConn(msg, w)

	case wspf.MessageTypePortForward:
		err = pf.HandleForward(msg, w)

	case wspf.MessageTypePortForwardStop:
		err = pf.Close()

	default:
		err = errPortForwardUnkonwnMessageType
	}

	if err != nil {
		errMsg := err.Error()
		b, _ := msgpack.Marshal(wspf.Error{
			Error:       &errMsg,
			MessageType: &msg.Header.MsgType,
		})
		w.WriteProtoMsg(&ws.ProtoMsg{
			Header: ws.ProtoHdr{
				Proto:     ws.ProtoTypePortForward,
				MsgType:   wspf.MessageTypeError,
				SessionID: msg.Header.SessionID,
			},
			Body: b,
		})
	}
}

func (pf *MenderPortForwarder) HandleNewConn(msg *ws.ProtoMsg, w ResponseWriter) error {
	var (
		req wspf.PortForwardNew
		err error
	)

	err = msgpack.Unmarshal(msg.Body, &req)
	if err != nil {
		return errors.Wrap(err, "error decoding message")
	}

	// Validate request
	if req.RemotePort == nil {
		return errors.New("invalid port forward request: remote_port: cannot be blank")
	}

	if req.Protocol == nil {
		proto := wspf.PortForwardProtocol(wspf.PortForwardProtocolTCP)
		req.Protocol = &proto
	}

	if req.RemoteHost == nil {
		emptyHost := ""
		req.RemoteHost = &emptyHost
	}

	// Validate preconditions
	if pf.conn != nil {
		return errors.New("port-forwarding is already active on this session")
	}

	pf.conn, err = net.Dial(string(*req.Protocol), fmt.Sprintf("%s:%d", *req.RemoteHost, *req.RemotePort))
	if err != nil {
		return errors.Wrap(err, "failed to open port")
	}
	go pf.Reader(w, msg.Header.SessionID)
	return nil
}

func (pf *MenderPortForwarder) Reader(w ResponseWriter, sessionID string) {
	buf := make([]byte, portForwardBuffSize)
	for n, err := pf.conn.Read(buf); err == nil; n, err = pf.conn.Read(buf) {
		select {
		case <-pf.closed:
			return
		default:
		}
		err = w.WriteProtoMsg(&ws.ProtoMsg{
			Header: ws.ProtoHdr{
				Proto:     ws.ProtoTypePortForward,
				MsgType:   wspf.MessageTypePortForward,
				SessionID: sessionID,
			},
			Body: buf[:n],
		})
		if err != nil {
			pf.Close()
			break
		}
	}
}

func (pf *MenderPortForwarder) HandleForward(msg *ws.ProtoMsg, w ResponseWriter) error {
	if pf.conn == nil {
		return errors.New("no port-forward active on this session")
	}
	select {
	case <-pf.closed:
		return errors.New("port-forwarding already closed")
	default:
	}
	_, err := pf.conn.Write(msg.Body)
	return errors.Wrap(err, "failed to forward packet to local connection")
}

func (pf *MenderPortForwarder) Close() error {
	var err error
	if pf.conn != nil {
		err = pf.conn.Close()
	}
	close(pf.closed)
	return err
}
