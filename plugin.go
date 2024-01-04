package crossover_limiter

import (
	"context"
	"encoding/json"
	"errors"
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
	DefaultTimeout      = 5
	UserExpiryInMinutes = 1
)

type User struct {
	limiter      *rate.Limiter
	expiresAt    time.Time
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

// ServeHTTP  serve http request for the users
func (a *RequestCrossoverLimiter) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	userId := a.extractUserID(req.URL.Path)
	if userId == "" {
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte("invalid requestId"))
		return
	}

	user, err := a.getUserPlan(userId)
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte("something went wrong"))
		return
	}
	if !user.limiter.Allow() {
		rw.WriteHeader(http.StatusTooManyRequests)
		rw.Write([]byte("too many requests"))
		return
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

// fetchUserPlan fetches user plan from third-party.
func (a *RequestCrossoverLimiter) fetchUserPlan(userId string) (int, error) {
	requestUrl, err := url.Parse(a.rateLimitPlanLimitUrl)
	if err != nil {
		log.Printf("FetchUserPlan:ParseUrl, %s", err.Error())
		return 0, errors.New("something went worng")
	}
	queryParams := url.Values{}
	queryParams.Set("userId", userId)
	requestUrl.RawQuery = queryParams.Encode()
	httpReq, err := http.NewRequest(http.MethodGet, requestUrl.String(), nil)
	if err != nil {
		log.Printf("FetchUserPlan:NewRequest, %s", err.Error())
		return 0, errors.New("something went wrong")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", a.apiKey)

	httpRes, err := a.client.Do(httpReq)
	defer httpRes.Body.Close()
	if err != nil {
		log.Printf("FetchUserPlan:Do, %s", err.Error())
		return 0, errors.New("something went wrong")
	}

	if httpRes.StatusCode != http.StatusOK {
		log.Printf("FetchUserPlan:InvalidStatusCode: %d", httpRes.StatusCode)
		return 0, errors.New("something went wrong")
	}

	var response RateLimitResponse
	if err = json.NewDecoder(httpRes.Body).Decode(&response); err != nil {
		log.Printf("FetchUserPlan:UNMARSHAERPlan, %s", err.Error())
		return 0, errors.New("something went wrong")
	}

	return response.Data.RequestLimit, nil
}

// setUserPlan store user plan to the sync.map
func (a *RequestCrossoverLimiter) setUserPlan(userId string) (*User, error) {
	requestLimit, err := a.fetchUserPlan(userId)
	if err != nil {
		return nil, err
	}
	u := &User{
		limiter:      rate.NewLimiter(rate.Limit(requestLimit), requestLimit),
		requestLimit: requestLimit,
		expiresAt:    time.Now().Add(UserExpiryInMinutes * time.Minute),
	}
	users.Store(userId, u)
	return u, nil
}

// getUserPlan load user plan from sync.map or set's it if not found
func (a *RequestCrossoverLimiter) getUserPlan(userId string) (*User, error) {
	if u, found := users.Load(userId); found {
		user := u.(*User)
		if time.Now().After(user.expiresAt) { //refresh user plan in non-blocking way
			go a.refreshUserPlan(userId)
		}
		return user, nil
	}

	return a.setUserPlan(userId)
}

// refreshUserPlan refreshes the user's rate limit plan
func (a *RequestCrossoverLimiter) refreshUserPlan(userId string) (*User, error) {
	requestLimit, err := a.fetchUserPlan(userId)
	if err != nil {
		return nil, err
	}
	if u, found := users.Load(userId); found {
		user := u.(*User)
		// check if the fetched plan has a different request limit.
		if user.requestLimit != requestLimit {
			// update the user's limiter with the new rate limit.
			user.limiter.SetLimit(rate.Limit(requestLimit))
			user.limiter.SetBurst(requestLimit)
			user.requestLimit = requestLimit
			user.expiresAt = time.Now().Add(UserExpiryInMinutes * time.Minute)
			users.Store(userId, user)
			return user, nil
		}
		return user, nil
	} else {
		// if user is not found it could be a new user.
		return a.setUserPlan(userId)
	}
}
