package main

import (
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type commandDropClient struct {
	mqtt.Client
	commandHandler mqtt.MessageHandler
}

func (c *commandDropClient) Publish(string, byte, bool, interface{}) mqtt.Token {
	return nil
}

func (c *commandDropClient) Subscribe(topic string, _ byte, handler mqtt.MessageHandler) mqtt.Token {
	if topic == mqttBaseTopic+"/+/+/set" {
		c.commandHandler = handler
	}
	return nil
}

type commandDropMessage struct {
	mqtt.Message
}

func (commandDropMessage) Topic() string { return mqttBaseTopic + "/camera/light/set" }

func (commandDropMessage) Payload() []byte {
	panic("dropped commands must not read the payload")
}

type commandAcceptedMessage struct {
	commandDropMessage
	reads int
}

func (m *commandAcceptedMessage) Payload() []byte {
	m.reads++
	return []byte("ON")
}

func TestMQTTCommandReleasesSlotAfterHandlerReturns(t *testing.T) {
	// Reserve all but one slot: a leaked slot from either command will fill the
	// semaphore and prevent the next command's payload from being read.
	for i := 0; i < cap(mqttCommandSlots)-1; i++ {
		mqttCommandSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(mqttCommandSlots)-1; i++ {
			<-mqttCommandSlots
		}
	})
	b := &haBridgeState{}
	c := &commandDropClient{}
	b.onConnect(c)
	if c.commandHandler == nil {
		t.Fatal("command callback was not subscribed")
	}
	for i := 0; i < 2; i++ {
		m := &commandAcceptedMessage{}
		c.commandHandler(c, m)
		if m.reads != 1 {
			t.Fatalf("command %d was not accepted: payload reads=%d", i+1, m.reads)
		}
		deadline := time.Now().Add(time.Second)
		for len(mqttCommandSlots) != cap(mqttCommandSlots)-1 {
			if time.Now().After(deadline) {
				t.Fatalf("command %d did not release its slot", i+1)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestMQTTCommandDropsWhenHandlersBusy(t *testing.T) {
	for i := 0; i < cap(mqttCommandSlots); i++ {
		mqttCommandSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(mqttCommandSlots); i++ {
			<-mqttCommandSlots
		}
	})

	b := &haBridgeState{}
	c := &commandDropClient{}
	b.onConnect(c)
	if c.commandHandler == nil {
		t.Fatal("command callback was not subscribed")
	}
	done := make(chan struct{})
	go func() {
		c.commandHandler(c, commandDropMessage{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("command callback blocked when all handler slots were busy")
	}
	if got := len(mqttCommandSlots); got != cap(mqttCommandSlots) {
		t.Fatalf("dropped command changed occupied slots: got %d, want %d", got, cap(mqttCommandSlots))
	}
}
