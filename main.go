package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	raven "github.com/getsentry/raven-go"
	"github.com/google/uuid"
)

type Config struct {
	MQTTBroker       string `json:"mqtt_broker"`
	MQTTCommandTopic string `json:"mqtt_command_topic"`
	MQTTStateTopic   string `json:"mqtt_state_topic"`
	Password         string `json:"password"`
	Username         string `json:"username"`
	SentryDSN        string `json:"sentry_dsn"`
}

type Simplisafe struct {
	client     *http.Client
	deviceUUID string
	userID     string
	locations  *LocationsResponse
}

func New() *Simplisafe {
	deviceUUID := uuid.New().String()
	cjar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: cjar,
	}

	return &Simplisafe{
		client:     client,
		deviceUUID: deviceUUID,
	}
}

func (s *Simplisafe) MustGetUserID() string {
	if s.userID == "" {
		panic("Not loggged in")
	}

	return s.userID
}

func (s *Simplisafe) MustGetLocation() string {
	if s.locations == nil {
		panic("GetStatus not called yet, or no location in Simplisafe account")
	}

	for loc := range s.locations.Locations {
		return loc
	}

	panic("No location in Simplisafe account")
}

func (s *Simplisafe) Login(username, password string) error {
	resp, postErr := s.client.PostForm("https://simplisafe.com/mobile/login", url.Values{
		"name":        {username},
		"pass":        {password},
		"version":     {"1200"},
		"device_uuid": {s.deviceUUID},
		"device_name": {"Go API"},
	})
	defer resp.Body.Close()
	if postErr != nil {
		return postErr
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Wrong status code %d while logging in", resp.StatusCode)
	}

	decoder := json.NewDecoder(resp.Body)
	var loginResp LoginResponse
	decodeErr := decoder.Decode(&loginResp)
	if decodeErr != nil {
		return decodeErr
	}
	s.userID = loginResp.UID
	_, _ = s.GetStatus()
	return nil
}

func (s *Simplisafe) GetStatus() (string, error) {
	resp, postErr := s.client.PostForm(fmt.Sprintf("https://simplisafe.com/mobile/%s/locations", s.MustGetUserID()), url.Values{
		"no_persist":           {"1"},
		"XDEBUG_SESSION_START": {"session_name"},
	})
	defer resp.Body.Close()
	if postErr != nil {
		return "", postErr
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Wrong status code %d while listing locations", resp.StatusCode)
	}
	decoder := json.NewDecoder(resp.Body)
	var locationsResp LocationsResponse
	if decodeErr := decoder.Decode(&locationsResp); decodeErr != nil {
		return "", decodeErr
	}

	s.locations = &locationsResp
	return locationsResp.GetSingleStatus(), nil
}

func translateMQTTStatus(mqttStatus string) string {
	switch strings.ToLower(mqttStatus) {
	case "disarm", "disarmed":
		return "off"
	case "arm_away":
		return "away"
	case "arm_home":
		return "home"
	}

	panic(fmt.Sprintf("Unknown status %s", mqttStatus))
}

func (s *Simplisafe) SetStatus(status string) error {
	tStatus := translateMQTTStatus(status)
	if tStatus == s.locations.GetSingleStatus() {
		log.Println("No status change required")
		return nil
	}
	resp, postErr := s.client.PostForm(fmt.Sprintf("https://simplisafe.com/mobile/%s/sid/%s/set-state", s.MustGetUserID(), s.MustGetLocation()), url.Values{
		"state":                {tStatus},
		"mobile":               {"1"},
		"no_persist":           {"1"},
		"XDEBUG_SESSION_START": {"session_name"},
	})
	if postErr != nil {
		return postErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Wrong status code %d while setting status", resp.StatusCode)
	}
	return nil
}

type LoginResponse struct {
	ReturnCode int    `json:"return_code"`
	Session    string `json:"session"`
	UID        string `json:"uid"`
	Username   string `json:'username"`
}

type LocationsResponse struct {
	NumLocations int `json:"num_locations"`
	Locations    map[string]struct {
		Street1     string `json:"street1"`
		Street2     string `json:"street2"`
		City        string `json:"city"`
		State       string `json:"state"`
		PostalCode  string `json:"postal_code"`
		StatusCode  int    `json:"s_status,string"`
		SystemState string `json:"system_state"`
	} `json:"locations"`
}

func (l LocationsResponse) GetSingleStatus() string {
	for _, value := range l.Locations {
		switch value.SystemState {
		case "Off":
			return "disarmed"
		case "Home":
			return "armed_home"
		case "Away":
			return "armed_away"
		default:
			return ""
		}
	}
	return ""
}

func readConfig(path string) (*Config, error) {
	if f, err := os.Open(path); err != nil {
		return nil, err
	} else {
		var config Config
		decoder := json.NewDecoder(f)
		if decErr := decoder.Decode(&config); decErr != nil {
			return nil, decErr
		} else {
			return &config, nil
		}
	}
}

func main() {
	configFile := flag.String("config", "/config/config.json", "Config file (containing login credentials)")
	flag.Parse()

	config, configErr := readConfig(*configFile)
	if configErr != nil {
		// Sentry not set up yet - can't log there.
		log.Fatal(configErr)
	}

	raven.SetDSN(config.SentryDSN)

	opts := mqtt.NewClientOptions()
	opts.AddBroker(config.MQTTBroker)
	opts.SetClientID("simplimqtt")
	mqttClient := mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		raven.CaptureErrorAndWait(token.Error(), nil)
		log.Fatalf("Error connecting to MQTT: %s", token.Error())
	}

	simplisafe := New()
	if loginErr := simplisafe.Login(config.Username, config.Password); loginErr != nil {
		raven.CaptureErrorAndWait(loginErr, nil)
		log.Fatal(loginErr)
	}

	mqttClient.Subscribe(config.MQTTCommandTopic, byte(2), func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("* [%s] %s\n", msg.Topic(), string(msg.Payload()))
		setStatusErr := simplisafe.SetStatus(string(msg.Payload()))
		if setStatusErr != nil {
			raven.CaptureErrorAndWait(setStatusErr, nil)
		}
	})

	for {
		status, sErr := simplisafe.GetStatus()
		if sErr != nil {
			raven.CaptureErrorAndWait(sErr, nil)
			log.Fatal(sErr)
		}

		mqttClient.Publish(
			config.MQTTStateTopic,
			byte(1), // QOS
			true,
			status,
		).Wait()

		time.Sleep(15 * time.Second)
	}
}
