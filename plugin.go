package crossover_limiter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

const (
	DefaultTimeout       = 5
	DefaultRedisPoolSize = 10
	UserPlanKeySuffix    = "-plan"
	UserRateKeySuffix    = "-rate"
)

type RateLimitResponse struct {
	Data struct {
		RequestLimit int `json:"request_limit"`
	} `json:"data"`
}

// Config holds configuration to passed to the plugin
type Config struct {
	RequestIdPattern      string
	RateLimitPlanLimitURL string
	APIKey                string
	RedisAuth             string
	RedisAddress          string
	RedisPoolSize         int
}

// CreateConfig populates the config data object
func CreateConfig() *Config {
	return &Config{}
}

type RequestCrossoverLimiter struct {
	next                  http.Handler
	name                  string
	client                *http.Client
	redisClient           *RedisClient
	compiledPattern       *regexp.Regexp
	rateLimitPlanLimitUrl string
	apiKey                string
	redisAuth             string
	redisAddress          string
	redisPoolSize         int
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
	if len(config.RedisAddress) == 0 {
		return nil, fmt.Errorf("RedisAddress can't be empty")
	}
	if config.RedisPoolSize == 0 {
		config.RedisPoolSize = DefaultRedisPoolSize
	}

	client := &http.Client{
		Timeout: DefaultTimeout * time.Second,
	}
	compiledPattern := regexp.MustCompile(config.RequestIdPattern)

	redisClient := NewRedisClient(config.RedisAddress, config.RedisPoolSize, config.RedisAuth)

	requestHandler := &RequestCrossoverLimiter{
		next:                  next,
		name:                  name,
		client:                client,
		compiledPattern:       compiledPattern,
		rateLimitPlanLimitUrl: config.RateLimitPlanLimitURL,
		apiKey:                config.APIKey,
		redisClient:           redisClient,
		redisAddress:          config.RedisAddress,
		redisAuth:             config.RedisAuth,
		redisPoolSize:         config.RedisPoolSize,
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
	userRequestLimit, err := a.getUserPlan(userId)
	if err != nil {
		log.Println(err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte("something went wrong"))
		return
	}
	if userRequestLimit == 0 { //setUserLimit
		//fetch user plan
		limit, err := a.fetchUserPlan(userId)
		if err != nil {
			log.Println(err.Error())
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("something went wrong"))
			return
		}
		//set user plan
		err = a.setUserPlan(userId, limit)
		if err != nil {
			log.Println(err.Error())
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("something went wrong"))
			return
		}
		userRequestLimit = limit
	}
	allow, err := a.rateLimiter(fmt.Sprintf("%s%s", userId, UserRateKeySuffix), userRequestLimit, 1)
	if err != nil {
		log.Println(err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte("something went wrong"))
		return
	}
	if !allow {
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

// setUserPlan store user plan from storage
func (a *RequestCrossoverLimiter) setUserPlan(userId string, requestLimit int) error {
	return a.redisClient.Set(fmt.Sprintf("%s%s", userId, UserPlanKeySuffix), strconv.Itoa(requestLimit))
}

// getUserPlan load user plan from storage
func (a *RequestCrossoverLimiter) getUserPlan(userId string) (int, error) {
	limitStr, err := a.redisClient.Get(fmt.Sprintf("%s%s", userId, UserPlanKeySuffix))
	if err != nil {
		return 0, err
	}
	if limitStr == "" {
		return 0, nil
	}
	// Parse the user plan.
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		return 0, errors.New(fmt.Sprintf("can't parse userId: %s, plan coz the err: %s", userId, err.Error()))
	}
	return limit, nil
}

func (a *RequestCrossoverLimiter) rateLimiter(key string, limit int, window int) (bool, error) {
	// Increment the counter for the given key.
	count, err := a.redisClient.Incr(key)
	if err != nil {
		return false, err
	}
	if count == 1 {
		// If the key is new or expired (i.e., count == 1), set the expiration.
		_, err = a.redisClient.Expire(key, window)
		if err != nil {
			return false, err
		}
	}

	// Check against the limit.
	return count <= limit, nil
}
