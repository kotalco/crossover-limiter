package crossover_limiter

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const (
	defaultTimeout               = 5
	defaultAPIKEY                = "c499a9cf54b4f5b8281762802b55462a8d020c835e6795ce4d1b6d268f6e32a5"
	defaultRateLimitPlanLimitURL = "http://localhost:8083/api/v1/subscriptions/request-limit"
	defaultGetRequestIdPattern   = "([a-z0-9]{42})"
)

type client struct {
	limiter      *rate.Limiter
	lastSeen     time.Time
	requestLimit int
}

var (
	clients = make(map[string]*client)
)

// Config holds configuration to passed to the plugin
type Config struct {
	RequestIdPattern      string
	RateLimitPlanLimitURL string
	APIKey                string
}

// CreateConfig populates the config data object
func CreateConfig() *Config {
	return &Config{
		RequestIdPattern:      defaultGetRequestIdPattern,
		RateLimitPlanLimitURL: defaultRateLimitPlanLimitURL,
		APIKey:                defaultAPIKEY,
	}
}

type RequestCrossoverLimiter struct {
	next                  http.Handler
	name                  string
	client                http.Client
	requestIdPattern      string
	rateLimitPlanLimitUrl string
	apiKey                string
}

// New created a new  plugin.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if len(config.APIKey) == 0 {
		return nil, fmt.Errorf("APIKey can't be empty")
	}
	if len(config.RequestIdPattern) == 0 {
		return nil, fmt.Errorf("GetRequestIdPattern can't be empty")
	}
	if len(config.RateLimitPlanLimitURL) == 0 {
		return nil, fmt.Errorf("RateLimitPlanLimitURL can't be empty")
	}

	requestHandler := &RequestCrossoverLimiter{
		next: next,
		name: name,
		client: http.Client{
			Timeout: defaultTimeout * time.Second,
		},
		requestIdPattern:      config.RequestIdPattern,
		rateLimitPlanLimitUrl: config.RateLimitPlanLimitURL,
		apiKey:                config.APIKey,
	}
	go requestHandler.cleanUp()
	return requestHandler, nil
}

func (a *RequestCrossoverLimiter) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	userId := a.extractUserID(req)
	if userId == "" {
		rw.WriteHeader(http.StatusBadRequest)
		// Write the error message to the response writer
		rw.Write([]byte("invalid requestId"))
		return
	}
	if !a.limit(userId, a.getRateLimitPlan(userId)) {
		rw.WriteHeader(http.StatusTooManyRequests)
		rw.Write([]byte("too many requests"))
		return
	}
	a.next.ServeHTTP(rw, req)
}

// extractUserID extract user id from the request
func (a *RequestCrossoverLimiter) extractUserID(req *http.Request) string {
	// Find the first match of the pattern in the URL Path
	match := regexp.MustCompile(a.requestIdPattern).FindStringSubmatch(req.URL.Path)
	if len(match) == 0 {
		return ""
	}
	parsedUUID, err := uuid.Parse(match[0][10:])
	if err != nil {
		return ""
	}
	return parsedUUID.String()
}

// getRateLimitPlan gets the rate limit plan for a user.
func (a *RequestCrossoverLimiter) getRateLimitPlan(userId string) int {
	if _, found := clients[userId]; !found {
		a.setRateLimitPlan(userId)
	}
	return clients[userId].requestLimit
}

// setRateLimitPlan fetches and sets the rate limit plan for a user.
func (a *RequestCrossoverLimiter) setRateLimitPlan(userId string) {
	requestLimit := a.fetchAndUpdateRateLimitPlan(userId)
	lastSeen := time.Now()
	if v, found := clients[userId]; found {
		lastSeen = v.lastSeen
	}
	clients[userId] = &client{limiter: rate.NewLimiter(rate.Limit(requestLimit), requestLimit), requestLimit: requestLimit, lastSeen: lastSeen}
}

// fetchAndUpdateRateLimitPlan fetches and updates the rate limit plan for a user.
func (a *RequestCrossoverLimiter) fetchAndUpdateRateLimitPlan(userId string) int {
	requestUrl, err := url.Parse(a.rateLimitPlanLimitUrl)
	if err != nil {
		log.Println("HTTPCALLERERRPlan:", err.Error())
		return 0
	}
	queryParams := url.Values{}
	queryParams.Set("userId", "31ff56b7-56cd-43a0-8cb7-33980f6c3200")
	requestUrl.RawQuery = queryParams.Encode()
	httpReq, err := http.NewRequest(http.MethodGet, requestUrl.String(), nil)
	if err != nil {
		fmt.Println("HTTPCALLERERRPlan:", err.Error())
		return 0
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", a.apiKey)

	httpRes, err := a.client.Do(httpReq)
	if err != nil {
		log.Printf("HTTPDOERRPlan: %s", err.Error())
		return 0
	}

	if httpRes.StatusCode != http.StatusOK {
		return 0
	}

	body, err := ioutil.ReadAll(httpRes.Body)

	if err != nil {
		log.Printf("PlanPasreBody: %s", err.Error())
		return 0
	}

	var response map[string]map[string]int
	err = json.Unmarshal(body, &response)
	if err != nil {
		log.Printf("UNMARSHAERPlan: %s", err.Error())
		return 0
	}

	return response["data"]["request_limit"]
}

// cleanUp periodically cleans up idle users and refreshes the rate limit plan for active users.
func (a *RequestCrossoverLimiter) cleanUp() {
	for {
		time.Sleep(1 * time.Minute)
		for userId, client := range clients {
			if time.Since(client.lastSeen) > 5*time.Minute {
				delete(clients, userId)
			} else {
				a.setRateLimitPlan(userId)
			}
		}
	}
}

// limit checks if the user has exceeded the rate limit.
func (a *RequestCrossoverLimiter) limit(userId string, requestLimit int) bool {
	if _, found := clients[userId]; !found {
		clients[uuid.NewString()] = &client{limiter: rate.NewLimiter(rate.Limit(requestLimit), requestLimit), requestLimit: requestLimit}
	}
	clients[userId].lastSeen = time.Now()
	if !clients[userId].limiter.Allow() {
		return false
	}
	return true
}
