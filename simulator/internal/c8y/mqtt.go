package c8y

import (
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Device is a SmartREST 2.0 MQTT connection for a single device. The MQTT
// client ID is mapped by Cumulocity to the c8y_Serial external ID, so a device
// provisioned over REST with the same serial is reused.
type Device struct {
	serial string
	client mqtt.Client
}

// Connect opens an MQTT connection for the given serial (client ID).
func Connect(host, user, password, serial string) (*Device, error) {
	broker := fmt.Sprintf("ssl://%s:8883", strings.Split(host, ":")[0])
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(serial).
		SetUsername(user).
		SetPassword(password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(20 * time.Second).
		SetKeepAlive(60 * time.Second)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.WaitTimeout(30*time.Second) && token.Error() != nil {
		return nil, fmt.Errorf("mqtt: connect %s as %s: %w", broker, serial, token.Error())
	} else if token.Error() == nil && !client.IsConnected() {
		return nil, fmt.Errorf("mqtt: connect %s as %s: timeout", broker, serial)
	}
	return &Device{serial: serial, client: client}, nil
}

// Close disconnects the MQTT connection.
func (d *Device) Close() {
	d.client.Disconnect(500)
}

func (d *Device) publish(msg string) error {
	token := d.client.Publish("s/us", 1, false, msg)
	if !token.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("mqtt: publish %q: timeout", msg)
	}
	return token.Error()
}

func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// Measurement publishes a single series value (SmartREST template 200).
func (d *Device) Measurement(fragment, series string, value float64, unit string) error {
	return d.publish(fmt.Sprintf("200,%s,%s,%.2f,%s", fragment, series, value, unit))
}

var mqttAlarmTemplate = map[string]string{
	SeverityCritical: "301",
	SeverityMajor:    "302",
	SeverityMinor:    "303",
	SeverityWarning:  "304",
}

// Alarm raises an alarm via SmartREST templates 301-304.
func (d *Device) Alarm(severity, alarmType, text string) error {
	tpl, ok := mqttAlarmTemplate[severity]
	if !ok {
		return fmt.Errorf("mqtt: unsupported severity %q", severity)
	}
	return d.publish(fmt.Sprintf("%s,%s,%s", tpl, alarmType, csvEscape(text)))
}

// ClearAlarm clears all alarms of the given type (SmartREST template 306).
func (d *Device) ClearAlarm(alarmType string) error {
	return d.publish(fmt.Sprintf("306,%s", alarmType))
}

// Event creates an event (SmartREST template 400).
func (d *Device) Event(eventType, text string) error {
	return d.publish(fmt.Sprintf("400,%s,%s", eventType, csvEscape(text)))
}
