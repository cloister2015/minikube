/*
Copyright 2016 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sshutil

import (
	"strings"
	"testing"

	"github.com/docker/machine/libmachine/drivers"

	"k8s.io/minikube/pkg/minikube/tests"
)

func TestNewSSHClient(t *testing.T) {
	s, _ := tests.NewSSHServer()
	port, err := s.Start()
	if err != nil {
		t.Fatalf("Error starting ssh server: %s", err)
	}
	d := &tests.MockDriver{
		Port: port,
		BaseDriver: drivers.BaseDriver{
			IPAddress:  "127.0.0.1",
			SSHKeyPath: "",
		},
	}
	c, err := NewSSHClient(d)
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}

	cmd := "foo"
	RunCommand(c, cmd)
	if !s.Connected {
		t.Fatalf("Error!")
	}

	if !strings.Contains(s.Commands[0], cmd) {
		t.Fatalf("Expected command: %s, got %s", cmd, s.Commands[0])
	}
}

func TestNewSSHHost(t *testing.T) {
	sshKeyPath := "mypath"
	ip := "localhost"
	user := "myuser"
	d := tests.MockDriver{
		BaseDriver: drivers.BaseDriver{
			IPAddress:  ip,
			SSHUser:    user,
			SSHKeyPath: sshKeyPath,
		},
	}

	h, err := newSSHHost(&d)
	if err != nil {
		t.Fatalf("Unexpected error creating host: %s", err)
	}

	if h.SSHKeyPath != sshKeyPath {
		t.Fatalf("%s != %s", h.SSHKeyPath, sshKeyPath)
	}
	if h.Username != user {
		t.Fatalf("%s != %s", h.Username, user)
	}
	if h.IP != ip {
		t.Fatalf("%s != %s", h.IP, ip)
	}
}

func TestNewSSHHostError(t *testing.T) {
	d := tests.MockDriver{HostError: true}

	_, err := newSSHHost(&d)
	if err == nil {
		t.Fatal("Expected error creating host, got nil")
	}
}

func TestTransfer(t *testing.T) {
	s, _ := tests.NewSSHServer()
	port, err := s.Start()
	if err != nil {
		t.Fatalf("Error starting ssh server: %s", err)
	}
	d := &tests.MockDriver{
		Port: port,
		BaseDriver: drivers.BaseDriver{
			IPAddress:  "127.0.0.1",
			SSHKeyPath: "",
		},
	}
	c, err := NewSSHClient(d)
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}

	dest := "bar"
	if err := Transfer([]byte("testcontents"), "/tmp", dest, "0777", c); err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}
}
