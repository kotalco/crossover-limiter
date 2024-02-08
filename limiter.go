package crossover_limiter

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"log"
	"net/http"
	"regexp"
)

const (
	DefaultRedisPoolSize = 10
	UserRateKeySuffix    = "-rate"
)

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

type Limiter struct {
	next            http.Handler
	name            string
	compiledPattern *regexp.Regexp
	planCache       IPlanCache
	planProxy       IPlanProxy
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

	compiledPattern := regexp.MustCompile(config.RequestIdPattern)
	planCache := NewPlanCache(config.RedisAddress, config.RedisAuth, config.RedisPoolSize)
	planProxy := NewPlanProxy(config.APIKey, config.RateLimitPlanLimitURL)

	handler := &Limiter{
		next:            next,
		name:            name,
		compiledPattern: compiledPattern,
		planCache:       planCache,
		planProxy:       planProxy,
	}

	return handler, nil
}

// ServeHTTP  serve http request for the users
func (a *Limiter) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	userId := a.extractUserID(req.URL.Path)
	if userId == "" {
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte("invalid requestId"))
		return
	}

	userPlan, err := a.planCache.getUserPlan(req.Context(), userId)
	if err != nil {
		log.Println(err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte(err.Error()))
		return
	}
	if err == nil && userPlan == 0 {
		userPlan, err = a.planProxy.fetch(userId)
		if err != nil {
			log.Println(err.Error())
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("something went wrong"))
			return
		}
		//set user plan
		err = a.planCache.setUserPlan(req.Context(), userId, userPlan)
		if err != nil {
			log.Println(err.Error())
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("something went wrong"))
			return
		}
	}

	key := fmt.Sprintf("%s%s", userId, UserRateKeySuffix)
	allow, err := a.planCache.limit(req.Context(), key, userPlan, 1)
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
func (a *Limiter) extractUserID(path string) (userId string) {
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
