package crossover_limiter

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"
)

const (
	DefaultTimeout           = 5
	RefreshLastSeenInMinutes = 4
)

type user struct {
	limiter      *rate.Limiter
	lastSeen     time.Time
	requestLimit int
}
type RateLimitResponse struct {
	Data struct {
		RequestLimit int `json:"request_limit"`
	} `json:"data"`
}

var (
	users sync.Map // users is now a sync.Map
)

// Config holds configuration to passed to the plugin
type Config struct {
	RequestIdPattern      string
	RateLimitPlanLimitURL string
	APIKey                string
}

// CreateConfig populates the config data object
func CreateConfig() *Config {
	return &Config{}
}

type RequestCrossoverLimiter struct {
	next                  http.Handler
	name                  string
	client                *http.Client
	compiledPattern       *regexp.Regexp
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

	client := &http.Client{
		Timeout: DefaultTimeout * time.Second,
	}
	compiledPattern := regexp.MustCompile(config.RequestIdPattern)

	requestHandler := &RequestCrossoverLimiter{
		next:                  next,
		name:                  name,
		client:                client,
		compiledPattern:       compiledPattern,
		rateLimitPlanLimitUrl: config.RateLimitPlanLimitURL,
		apiKey:                config.APIKey,
	}
	return requestHandler, nil
}

func (a *RequestCrossoverLimiter) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	userId := a.extractUserID(req.URL.Path)
	if userId == "" {
		rw.WriteHeader(http.StatusBadRequest)
		// Write the error message to the response writer
		rw.Write([]byte("invalid requestId"))
		return
	}
	if !a.limit(userId, a.getRateLimitPlan(userId)) {
		//rw.WriteHeader(http.StatusTooManyRequests)
		//rw.Write([]byte("too many requests"))
		//return
	}
	a.next.ServeHTTP(rw, req)
}

// extractUserID extract user id from the request
func (a *RequestCrossoverLimiter) extractUserID(path string) string {
	// Find the first match of the pattern in the URL Path
	match := a.compiledPattern.FindStringSubmatch(path)
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
	if u, found := users.Load(userId); found {
		user := u.(*user)
		return user.requestLimit
	}
	// If user is not found, set the rate limit plan.
	a.setRateLimitPlan(userId) // possible enhancment return user type
	if u, found := users.Load(userId); found {
		user := u.(*user)
		return user.requestLimit
	}
	return 0
}

// setRateLimitPlan fetches and sets the rate limit plan for a user.
func (a *RequestCrossoverLimiter) setRateLimitPlan(userId string) {
	requestLimit := a.fetchAndUpdateRateLimitPlan(userId)
	lastSeen := time.Now()

	u := &user{
		limiter:      rate.NewLimiter(rate.Limit(requestLimit), requestLimit),
		requestLimit: requestLimit,
		lastSeen:     lastSeen,
	}
	users.Store(userId, u)
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
	defer httpRes.Body.Close()
	if err != nil {
		log.Printf("HTTPDOERRPlan: %s", err.Error())
		return 0
	}

	if httpRes.StatusCode != http.StatusOK {
		log.Printf("HTTPDOERRPlanRetrunedStatusCode: %d", httpRes.StatusCode)
		return 0
	}

	var response RateLimitResponse
	if err = json.NewDecoder(httpRes.Body).Decode(&response); err != nil {
		log.Printf("UNMARSHAERPlan: %s", err.Error())
		return 0
	}

	return response.Data.RequestLimit
}

// limit checks if the user has exceeded the rate limit.
func (a *RequestCrossoverLimiter) limit(userId string, requestLimit int) bool {
	result, ok := users.Load(userId)
	if !ok {
		//store the new user limiter in a non-blocking way
		limiter := rate.NewLimiter(rate.Limit(requestLimit), requestLimit)
		users.Store(userId, &user{
			limiter:      limiter,
			requestLimit: requestLimit,
			lastSeen:     time.Now(),
		})
		return true
	}

	u := result.(*user)
	if u.limiter.Allow() {
		now := time.Now()
		if now.Sub(u.lastSeen) > time.Minute*RefreshLastSeenInMinutes { //refresh last seen in an interval less than the interval of clean-ups
			u.lastSeen = now
			users.Store(userId, u) //don't store unless very necessary.
		}
		return true
	}

	return true
}
