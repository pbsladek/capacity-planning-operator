package cirunner

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"
	"time"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type PortForwardSession struct {
	stopCh  chan struct{}
	readyCh chan struct{}
	errCh   chan error
	buffer  *bytes.Buffer
	mu      sync.Mutex
	closed  bool
}

func (s *PortForwardSession) WaitReady(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.readyCh:
		return nil
	case err := <-s.errCh:
		if err == nil {
			return fmt.Errorf("port-forward ended before ready")
		}
		return err
	case <-timer.C:
		return fmt.Errorf("timed out waiting for port-forward readiness")
	}
}

func (s *PortForwardSession) Logs() string {
	if s == nil || s.buffer == nil {
		return ""
	}
	return s.buffer.String()
}

func (s *PortForwardSession) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stopCh)
	s.mu.Unlock()
	select {
	case <-s.errCh:
	case <-time.After(2 * time.Second):
	}
}

func StartPodPortForward(c *Clients, namespace, podName string, localPort, remotePort int) (*PortForwardSession, error) {
	reqURL := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward").URL()

	transport, upgrader, err := spdy.RoundTripperFor(c.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("creating spdy roundtripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)

	stopCh := make(chan struct{}, 1)
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)
	buf := &bytes.Buffer{}
	ports := []string{fmt.Sprintf("%d:%d", localPort, remotePort)}

	forwarder, err := portforward.New(dialer, ports, stopCh, readyCh, buf, buf)
	if err != nil {
		return nil, fmt.Errorf("creating portforward: %w", err)
	}

	session := &PortForwardSession{
		stopCh:  stopCh,
		readyCh: readyCh,
		errCh:   errCh,
		buffer:  buf,
	}
	go func() {
		errCh <- forwarder.ForwardPorts()
	}()
	return session, nil
}
