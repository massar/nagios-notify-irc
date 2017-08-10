package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lrstanley/girc"
	"github.com/valyala/gorpc"
)

type Daemon struct{}

func (s *Daemon) Execute([]string) error {
	done := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < len(conf.Servers); i++ {
		wg.Add(1)
		conf.Servers[i].recv = make(chan *Event)
		go conf.Servers[i].setup(done, &wg)
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)

	time.Sleep(10 * time.Second)
	conf.Servers[0].recv <- &Event{
		Pings:   []string{"*"},
		Targets: []string{"*"},
		Text:    "THIS IS A TEST. IGNORE.",
	}

	dp := gorpc.NewDispatcher()
	dp.AddService("Daemon", &s)
	rpc := gorpc.NewUnixServer(conf.SocketFile, dp.NewHandlerFunc())
	err := rpc.Start()
	if err != nil {
		debug.Fatalf("rpc: %s", err)
	}

	<-sc
	rpc.Stop()
	close(done)
	wg.Wait()

	fmt.Println("\nexiting")
	return nil
}

type Event struct {
	Pings   []string // "*", "@", or list of users.
	Targets []string // "*" or list of users.
	Text    string
}

type Server struct {
	ID            string
	Hostname      string
	Password      string
	Bind          string
	Port          int
	TLS           bool
	TLSSkipVerify bool
	Channels      []string
	DisableColors bool
	Nick          string
	Name          string
	User          string
	SASLUser      string
	SASLPass      string

	log  *log.Logger
	recv chan *Event
}

func (s *Server) setup(done chan struct{}, wg *sync.WaitGroup) error {
	defer wg.Done()
	if s.ID == "" {
		return errors.New("empty server id specified")
	}

	s.log = log.New(os.Stdout, s.ID+": ", log.Ltime)

	if s.Port == 0 {
		s.Port = conf.DefaultPort
	}

	if s.Nick == "" {
		s.Nick = conf.DefaultNick
	}

	if s.Name == "" {
		s.Name = conf.DefaultName
	}

	if s.User == "" {
		s.User = conf.DefaultUser
	}

	s.log.Printf("%#v\n", s)

	ircConfig := girc.Config{
		Server:       s.Hostname,
		ServerPass:   s.Password,
		Port:         s.Port,
		Nick:         s.Nick,
		Name:         s.Name,
		User:         s.User,
		Bind:         s.Bind,
		SSL:          s.TLS,
		GlobalFormat: !s.DisableColors,
		TLSConfig:    &tls.Config{ServerName: s.Hostname, InsecureSkipVerify: s.TLSSkipVerify},
		RecoverFunc:  func(_ *girc.Client, e *girc.HandlerError) { s.log.Print(e.Error()) },
	}

	if s.SASLUser != "" || s.SASLPass != "" {
		ircConfig.SASL = &girc.SASLPlain{User: s.SASLUser, Pass: s.SASLPass}
	}

	client := girc.New(ircConfig)
	client.Handlers.AddBg(girc.ALL_EVENTS, s.onAll)
	client.Handlers.Add(girc.CONNECTED, s.onConnect)

	var wgDone sync.WaitGroup
	go func() {
		wgDone.Add(1)
		for {
			err := client.Connect()
			if err == nil {
				break
			}
			s.log.Printf("error: %s", err)
		}

		wgDone.Done()
	}()

	for {
		select {
		case <-done:
			goto done
		case e := <-s.recv:
			s.handle(client, e)
		}
	}

done:
	client.Close()
	wgDone.Wait()

	for i := 0; i < len(conf.Servers); i++ {
		close(conf.Servers[i].recv)
	}

	return nil
}

func (s *Server) handle(c *girc.Client, e *Event) {
	targets := []string{}
	for i := 0; i < len(e.Targets); i++ {
		if e.Targets[i] == "*" {
			targets = c.Channels()
			break
		}

		targets = append(targets, e.Targets[i])
	}

	var pingAll bool
	var pingOps bool
	for i := 0; i < len(e.Pings); i++ {
		if e.Pings[i] == "*" {
			pingAll = true
			break
		}

		if e.Pings[i] == "@" {
			pingOps = true
			break
		}
	}

	for i := 0; i < len(targets); i++ {
		channel := c.LookupChannel(targets[i])
		if channel == nil {
			s.log.Printf("requested send to unknown channel %q", targets[i])
			continue
		}

		if pingAll {
			c.Cmd.Message(targets[i], strings.Join(channel.UserList, " ")+":")
		} else if pingOps {
			users := channel.Admins(c)
			ops := []string{}
			for j := 0; j < len(users); j++ {
				ops = append(ops, users[j].Nick)
			}

			if len(ops) > 0 {
				c.Cmd.Message(targets[i], strings.Join(ops, " ")+":")
			}
		} else {
			c.Cmd.Message(targets[i], strings.Join(e.Pings, " ")+":")
		}

		c.Cmd.Message(targets[i], e.Text)
	}
}

func (s *Server) onConnect(c *girc.Client, e girc.Event) {
	for i := 0; i < len(s.Channels); i++ {
		if split := strings.SplitN(s.Channels[i], " ", 1); len(split) == 2 {
			c.Cmd.JoinKey(split[0], split[1])
			continue
		}

		c.Cmd.Join(s.Channels[i])
	}
}

func (s *Server) onAll(c *girc.Client, e girc.Event) {
	if out, ok := e.Pretty(); ok {
		s.log.Println(out)
	}
}
