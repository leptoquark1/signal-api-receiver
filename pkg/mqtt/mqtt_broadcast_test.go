package mqtt_test

import (
	"testing"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/kalbasit/signal-api-receiver/pkg/receiver"
)

const (
	TestTopic string = "dev/test/message"
)

func TestMqtt(t *testing.T) {
	var (
		connectionManager *autopaho.ConnectionManager
		testMessage       receiver.Message = receiver.Message{}
	)

	t.Run("mqtt is initialized with given parameters", func(t *testing.T) {
	})

	t.Run("mqtt publishes message on newMessage event", func(t *testing.T) {
	})
}
