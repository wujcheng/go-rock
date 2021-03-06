package main

import (
	"bufio"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

type ngServerConfig struct {
	servAddr   string
	clientAddr string
}

type ngServer struct {
	servs  map[int64]*ngConn
	sMutex sync.Mutex

	client ngConnInterface // 客户端链接
	seq    int64           // 系列号

	clientAddr string
	servAddr   string
}

func newNgServer(config *ngServerConfig) *ngServer {
	server := &ngServer{
		servs:      make(map[int64]*ngConn),
		sMutex:     sync.Mutex{},
		client:     &emptyNgConn{},
		seq:        1,
		clientAddr: config.clientAddr,
		servAddr:   config.servAddr,
	}
	return server
}

func (s *ngServer) start() {
	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		s.listenClient()
	}()

	go func() {
		defer wg.Done()
		s.listenServ()
	}()
	wg.Wait()
}

func (s *ngServer) nextSeq() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func (s *ngServer) listenServ() {
	l, err := net.Listen("tcp", s.servAddr)
	if err != nil {
		return
	}

	defer l.Close()

	for {
		c, err := l.Accept()
		if err != nil {
			break
		}
		go s.handleServ(c)
	}
}

func (s *ngServer) handleServ(c net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("panic serv", err)
		}
	}()

	seq := s.nextSeq()
	s.addServ(seq, c)
	defer s.closeSeq(seq)

	msg := newMsg(HEAD_NEWCONN, seq, []byte{})
	_, err := s.client.WriteMsg(msg)
	if err != nil {
		return
	}

	// 分发数据
	buff := make([]byte, MAX_PACK_LEN)
	for {
		n, err := c.Read(buff)
		if err != nil {
			break
		}
		msg := newMsg(HEAD_CONTENT, seq, buff[:n])
		n, err = s.client.WriteMsg(msg)

		if err != nil {
			break
		}
	}
}

func (s *ngServer) listenClient() {
	l, err := net.Listen("tcp", s.clientAddr)
	if err != nil {
		return
	}

	defer l.Close()

	for {
		c, err := l.Accept()
		if err != nil {
			break
		}
		go s.handleClient(c)
	}
}

func (s *ngServer) handleClient(c net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("client panic", err)
		}
	}()

	defer c.Close()
	defer func() {
		s.sMutex.Lock()
		defer s.sMutex.Unlock()
		for k, v := range s.servs {
			delete(s.servs, k)
			v.Close()
		}

		s.client = &emptyNgConn{}
	}()

	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	s.client = &ngConn{c}

	reader := bufio.NewReader(c)
	for {
		msg, err := readMsg(reader)
		if err != nil {
			log.Println("Server 解析消息体错误", err)
			return
		}

		switch int(msg.head) {
		case HEAD_CONTENT:
			seq := msg.seq
			conn := s.getServ(seq)
			if conn != nil {
				conn.Write(msg.buff)
			} else {
				// 发送关闭消息
				s.closeSeq(seq)
			}
		case HEAD_CLOSE:
			seq := msg.seq
			s.closeSeq(seq)
		case HEAD_KEEPALIVE:
			continue
		default:
			log.Println("Server 忽略消息", msg)
		}
	}
}

func (s *ngServer) getServ(seq int64) *ngConn {
	s.sMutex.Lock()
	defer s.sMutex.Unlock()

	if c, ok := s.servs[seq]; ok {
		return c
	}
	return nil
}

func (s *ngServer) addServ(seq int64, c net.Conn) *ngConn {
	s.sMutex.Lock()
	defer s.sMutex.Unlock()
	s.servs[seq] = &ngConn{c}
	return s.servs[seq]
}

func (s *ngServer) closeSeq(seq int64) {
	s.sMutex.Lock()
	defer s.sMutex.Unlock()

	if c, ok := s.servs[seq]; ok {
		delete(s.servs, seq)

		c.Close()

		closeMsg := newMsg(HEAD_CLOSE, seq, []byte{})
		s.client.WriteMsg(closeMsg)
	}
}
