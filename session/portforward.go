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
	"context"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/mendersoftware/go-lib-micro/ws"
	wspf "github.com/mendersoftware/go-lib-micro/ws/portforward"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const (
	protocolTCP = "tcp"
	protocolUDP = "udp"
)

const (
	portForwardBuffSize          = 4096
	portForwardConnectionTimeout = time.Second * 600
)

var (
	errPortForwardInvalidMessage     = errors.New("invalid port-forward message: missing connection_id, remort_port or protocol")
	errPortForwardUnkonwnMessageType = errors.New("unknown message type")
	errPortForwardUnkonwnConnection  = errors.New("unknown connection")
)

var portForwarders = make(map[string]*MenderPortForwarder)

type MenderPortForwarder struct {
	ConnectionID   string
	SessionID      string
	ResponseWriter ResponseWriter
	conn           net.Conn
	ctx            context.Context
	ctxCancel      context.CancelFunc
}

func (f *MenderPortForwarder) Connect(protocol string, host string, portNumber int) error {
	log.Debugf("port-forward[%s/%s] connect: %s/%s:%d", f.SessionID, f.ConnectionID, protocol, host, portNumber)

	if protocol == protocolTCP || protocol == protocolUDP {
		conn, err := net.Dial(protocol, host+":"+strconv.Itoa(portNumber))
		if err != nil {
			return err
		}
		f.conn = conn
	} else {
		return errors.New("unknown protocol: " + protocol)
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	f.ctx = ctx
	f.ctxCancel = cancelFunc

	go f.Read()

	return nil
}

func (f *MenderPortForwarder) Close(sendStopMessage bool) error {
	log.Debugf("port-forward[%s/%s] close", f.SessionID, f.ConnectionID)
	if sendStopMessage {
		m := &ws.ProtoMsg{
			Header: ws.ProtoHdr{
				Proto:     ws.ProtoTypePortForward,
				MsgType:   wspf.MessageTypePortForwardStop,
				SessionID: f.SessionID,
				Properties: map[string]interface{}{
					wspf.PropertyPortForwardConnectionID: f.ConnectionID,
				},
			},
		}
		if err := f.ResponseWriter.WriteProtoMsg(m); err != nil {
			log.Errorf("portForwardHandler: webSock.WriteMessage(%+v)", err)
		}
	}
	defer delete(portForwarders, f.SessionID+"/"+f.ConnectionID)
	f.ctxCancel()
	return f.conn.Close()
}

func (f *MenderPortForwarder) Read() {
	errChan := make(chan error)
	dataChan := make(chan []byte)

	go func() {
		data := make([]byte, portForwardBuffSize)

		for {
			n, err := f.conn.Read(data)
			if err != nil {
				errChan <- err
				break
			}
			if n > 0 {
				dataChan <- data[:n]
			}
		}
	}()

	for {
		select {
		case err := <-errChan:
			if err != io.EOF {
				log.Errorf("port-forward[%s/%s] error: %v\n", f.SessionID, f.ConnectionID, err.Error())
			}
			f.Close(true)
		case data := <-dataChan:
			log.Debugf("port-forward[%s/%s] read %d bytes", f.SessionID, f.ConnectionID, len(data))

			m := &ws.ProtoMsg{
				Header: ws.ProtoHdr{
					Proto:     ws.ProtoTypePortForward,
					MsgType:   wspf.MessageTypePortForward,
					SessionID: f.SessionID,
					Properties: map[string]interface{}{
						wspf.PropertyPortForwardConnectionID: f.ConnectionID,
					},
				},
				Body: data,
			}
			if err := f.ResponseWriter.WriteProtoMsg(m); err != nil {
				log.Errorf("portForwardHandler: webSock.WriteMessage(%+v)", err)
			}
		case <-time.After(portForwardConnectionTimeout):
			f.Close(true)
		case <-f.ctx.Done():
			return
		}
	}
}

func (f *MenderPortForwarder) Write(body []byte) error {
	log.Debugf("port-forward[%s/%s] write %d bytes", f.SessionID, f.ConnectionID, len(body))
	_, err := f.conn.Write(body)
	if err != nil {
		return err
	}
	return nil
}

func PortForward() Constructor {
	f := HandlerFunc(portForwardHandler)
	return func() SessionHandler { return f }
}

func portForwardHandler(msg *ws.ProtoMsg, w ResponseWriter) {
	var err error
	switch msg.Header.MsgType {
	case wspf.MessageTypePortForwardNew:
		err = portForwardHandlerNew(msg, w)
	case wspf.MessageTypePortForwardStop:
		err = portForwardHandlerStop(msg, w)
	case wspf.MessageTypePortForward:
		err = portForwardHandlerForward(msg, w)
	default:
		err = errPortForwardUnkonwnMessageType
	}
	if err != nil {
		log.Errorf("portForwardHandler(%+v)", err)

		response := &ws.ProtoMsg{
			Header: ws.ProtoHdr{
				Proto:      ws.ProtoTypePortForward,
				MsgType:    ws.MessageTypeError,
				SessionID:  msg.Header.SessionID,
				Properties: msg.Header.Properties,
			},
			Body: []byte(err.Error()),
		}
		if err := w.WriteProtoMsg(response); err != nil {
			log.Errorf("portForwardHandler: webSock.WriteMessage(%+v)", err)
		}
	}
}

func getConnectionIDFromMessage(message *ws.ProtoMsg) string {
	connectionID, _ := message.Header.Properties[wspf.PropertyPortForwardConnectionID].(string)
	return connectionID
}

func portForwardHandlerNew(message *ws.ProtoMsg, w ResponseWriter) error {
	connectionID := getConnectionIDFromMessage(message)
	protocol, _ := message.Header.Properties[wspf.PropertyPortForwardProtocol].(string)
	host, _ := message.Header.Properties[wspf.PropertyPortForwardRemoteHost].(string)
	portNumber, _ := message.Header.Properties[wspf.PropertyPortForwardRemotePort].(uint64)

	if connectionID == "" || portNumber == 0 || protocol == "" {
		return errPortForwardInvalidMessage
	}

	portForwarder := &MenderPortForwarder{
		ConnectionID:   connectionID,
		SessionID:      message.Header.SessionID,
		ResponseWriter: w,
	}

	key := message.Header.SessionID + "/" + connectionID
	portForwarders[key] = portForwarder

	log.Infof("port-forward: new %s: %s/%s:%d", key, protocol, host, portNumber)
	err := portForwarder.Connect(protocol, host, int(portNumber))
	if err != nil {
		delete(portForwarders, key)
		return err
	}

	response := &ws.ProtoMsg{
		Header: ws.ProtoHdr{
			Proto:     message.Header.Proto,
			MsgType:   message.Header.MsgType,
			SessionID: message.Header.SessionID,
			Properties: map[string]interface{}{
				wspf.PropertyPortForwardConnectionID: connectionID,
			},
		},
	}
	if err := w.WriteProtoMsg(response); err != nil {
		log.Errorf("portForwardHandler: webSock.WriteMessage(%+v)", err)
	}

	return nil
}

func portForwardHandlerStop(message *ws.ProtoMsg, w ResponseWriter) error {
	connectionID := getConnectionIDFromMessage(message)
	key := message.Header.SessionID + "/" + connectionID
	if portForwarder, ok := portForwarders[key]; ok {
		log.Infof("port-forward: stop %s", key)
		defer delete(portForwarders, key)
		if err := portForwarder.Close(false); err != nil {
			return err
		}

		response := &ws.ProtoMsg{
			Header: ws.ProtoHdr{
				Proto:     message.Header.Proto,
				MsgType:   message.Header.MsgType,
				SessionID: message.Header.SessionID,
				Properties: map[string]interface{}{
					wspf.PropertyPortForwardConnectionID: connectionID,
				},
			},
		}
		if err := w.WriteProtoMsg(response); err != nil {
			log.Errorf("portForwardHandler: webSock.WriteMessage(%+v)", err)
		}

		return nil
	} else {
		return errPortForwardUnkonwnConnection
	}
}

func portForwardHandlerForward(message *ws.ProtoMsg, w ResponseWriter) error {
	connectionID := getConnectionIDFromMessage(message)
	key := message.Header.SessionID + "/" + connectionID
	if portForwarder, ok := portForwarders[key]; ok {
		return portForwarder.Write(message.Body)
	} else {
		return errPortForwardUnkonwnConnection
	}
}
