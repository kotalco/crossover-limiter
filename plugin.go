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
	DefaultTimeout                = 5
	UsersRefreshIntervalInMinutes = 30
)

type User struct {
	id           string
	limiter      *rate.Limiter
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
	go requestHandler.scheduler(UsersRefreshIntervalInMinutes * time.Minute)
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

	user := a.getUserPlan(userId)
	if user == nil {
		requestLimit, err := a.fetchUserPlan(userId)
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("something went wrong"))
			return
		}
		user = a.setUserPlan(userId, requestLimit)
	}

	if !user.limiter.Allow() {
		rw.WriteHeader(http.StatusTooManyRequests)
		rw.Write([]byte("too many requests"))
		return
	}
	a.next.ServeHTTP(rw, req)
}

// extractUserID extract user id from the request
func (a *RequestCrossoverLimiter) extractUserID(path string) (userId string) {
	// Find the first match of the pattern in the URL Path
	match := a.compiledPattern.FindStringSubmatch(path)
	if len(match) == 0 {
		return
	}
	parsedUUID, err := uuid.Parse(match[0][10:])
	if err != nil {
		return
	}
	return parsedUUID.String()
}

// fetchUserPlan fetches user plan from third-party.
func (a *RequestCrossoverLimiter) fetchUserPlan(userId string) (int, error) {
	requestUrl, err := url.Parse(a.rateLimitPlanLimitUrl)
	if err != nil {
		log.Printf("FetchUserPlan:ParseUrl, %s", err.Error())
		return 0, errors.New("something went wrong")
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
func (a *RequestCrossoverLimiter) setUserPlan(userId string, requestLimit int) *User {
	u := &User{
		limiter:      rate.NewLimiter(rate.Limit(requestLimit), requestLimit),
		requestLimit: requestLimit,
		id:           userId,
	}
	users.Store(userId, u)
	return u
}

// getUserPlan load user plan from sync.map or set's it if not found
func (a *RequestCrossoverLimiter) getUserPlan(userId string) *User {
	if u, found := users.Load(userId); found {
		return u.(*User)
	}
	return nil
}

// refreshUserPlan refreshes the user's rate limit plan
func (a *RequestCrossoverLimiter) refreshUserPlan(user *User, newRequestLimit int) {
	if user.requestLimit != newRequestLimit {
		// update the user's limiter with the new rate limit.
		user.limiter.SetLimit(rate.Limit(newRequestLimit))
		user.limiter.SetBurst(newRequestLimit)
		user.requestLimit = newRequestLimit
		users.Store(user.id, user)
	}
}

// refreshPlansForAllUsers refreshes the rate limit plans for all users in the sync.map
func (a *RequestCrossoverLimiter) refreshPlansForAllUsers() {
	users.Range(func(key, u interface{}) bool {
		userId := key.(string)
		user := u.(*User)
		newReqLimit, err := a.fetchUserPlan(userId)
		if err != nil {
			log.Printf("REFRESH_PLAN_FOR_ALL_USERS: %s", err.Error())
			return true //continue iteration
		}
		a.refreshUserPlan(user, newReqLimit)
		return true // continue iteration
	})
}

// scheduler starts a periodic refresh of user plans.
func (a *RequestCrossoverLimiter) scheduler(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.refreshPlansForAllUsers()
		}
	}
}
