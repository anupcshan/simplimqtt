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
	client          *http.Client
	userID          int
	accessToken     string
	refreshToken    string
	tokenExpiration time.Time
	locations       *LocationsResponse
}

func New() *Simplisafe {
	cjar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: cjar,
	}

	return &Simplisafe{
		client: client,
	}
}

func (s *Simplisafe) MustGetUserID() int {
	if s.userID == 0 {
		panic("Not loggged in")
	}

	return s.userID
}

func (s *Simplisafe) MustGetLocation() int {
	if s.locations == nil {
		panic("GetStatus not called yet, or no location in Simplisafe account")
	}

	for _, sub := range s.locations.Subscriptions {
		return sub.SID
	}

	panic("No location in Simplisafe account")
}

func (s *Simplisafe) Login(username, password string) error {
	reqBody := strings.NewReader(url.Values{
		"username":   {username},
		"password":   {password},
		"device_id":  {"SimpliMQTT"},
		"grant_type": {"password"},
	}.Encode())

	req, err := http.NewRequest("POST", "https://api.simplisafe.com/v1/api/token", reqBody)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", "Basic NGRmNTU2MjctNDZiMi00ZTJjLTg2NmItMTUyMWIzOTVkZWQyLjEtMC0wLldlYkFwcC5zaW1wbGlzYWZlLmNvbTo=")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, postErr := s.client.Do(req)
	if postErr != nil {
		return postErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Wrong status code %d while logging in", resp.StatusCode)
	}

	decoder := json.NewDecoder(resp.Body)
	var loginResp LoginResponse
	decodeErr := decoder.Decode(&loginResp)
	if decodeErr != nil {
		return decodeErr
	}
	s.accessToken = loginResp.AccessToken
	s.refreshToken = loginResp.RefreshToken
	s.tokenExpiration = time.Now().Add(time.Duration(loginResp.ExpiresIn) * time.Second)
	return s.GetUserInfo()
}

type AuthCheckResponse struct {
	UserID int `json:"userId"`
	// IsAdmin bool `json:"isAdmin"`
}

func (s *Simplisafe) GetUserInfo() error {
	reqBody := strings.NewReader(url.Values{}.Encode())

	req, _ := http.NewRequest("GET", "https://api.simplisafe.com/v1/api/authCheck", reqBody)
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", s.accessToken))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, postErr := s.client.Do(req)
	if postErr != nil {
		return postErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Wrong status code %d while listing locations", resp.StatusCode)
	}
	decoder := json.NewDecoder(resp.Body)
	var authCheckResp AuthCheckResponse
	decodeErr := decoder.Decode(&authCheckResp)
	if decodeErr != nil {
		return decodeErr
	}
	s.userID = authCheckResp.UserID
	return nil
}

func (s *Simplisafe) GetStatus() (string, error) {
	reqBody := strings.NewReader(url.Values{}.Encode())

	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.simplisafe.com/v1/users/%d/subscriptions?activeOnly=false", s.MustGetUserID()), reqBody)
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", s.accessToken))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, postErr := s.client.Do(req)
	if postErr != nil {
		return "", postErr
	}
	defer resp.Body.Close()

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

	reqBody := strings.NewReader(url.Values{}.Encode())
	req, _ := http.NewRequest("POST", fmt.Sprintf("https://api.simplisafe.com/v1/ss3/subscriptions/%d/state/%s", s.MustGetLocation(), tStatus), reqBody)
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", s.accessToken))
	resp, postErr := s.client.Do(req)
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
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	// Scopes []int `json:"scopes"`
	// TokenType string `json:"token_type"`
}

type LocationsResponse struct {
	Subscriptions []struct {
		SID      int `json:"sid"`
		UID      int `json:"uid"`
		Location struct {
			Street1 string `json:"street1"`
			Street2 string `json:"street2"`
			City    string `json:"city"`
			State   string `json:"state"`
			Zip     string `json:"zip"`
			System  struct {
				AlarmState          string `json:"alarmState"`
				AlarmStateTimestamp uint64 `json:"alarmStateTimestamp"`
				StateUpdated        uint64 `json:"stateUpdated"`
			} `json:"system"`
		} `json:"location"`
	} `json:"subscriptions"`
}

func (l LocationsResponse) GetSingleStatus() string {
	for _, sub := range l.Subscriptions {
		switch sub.Location.System.AlarmState {
		case "OFF":
			return "disarmed"
		case "HOME":
			return "armed_home"
		case "AWAY_COUNT", "AWAY":
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
	log.SetFlags(log.Lmicroseconds | log.Lshortfile)

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

	if config.MQTTCommandTopic != "" {
		log.Printf("Subscribing to %s", config.MQTTCommandTopic)

		mqttClient.Subscribe(config.MQTTCommandTopic, byte(2), func(client mqtt.Client, msg mqtt.Message) {
			log.Printf("Received [%s] %s\n", msg.Topic(), string(msg.Payload()))
			setStatusErr := simplisafe.SetStatus(string(msg.Payload()))
			if setStatusErr != nil {
				raven.CaptureErrorAndWait(setStatusErr, nil)
			}
		})
	}

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

		time.Sleep(10 * time.Second)
	}
}
