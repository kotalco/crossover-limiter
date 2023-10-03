package crossover_limiter

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	defaultTimeout               = 5
	defaultAPIKEY                = "c499a9cf54b4f5b8281762802b55462a8d020c835e6795ce4d1b6d268f6e32a5"
	defaultRateLimitPlanLimitURL = "http://localhost:8083/api/v1/subscriptions/:userId/request-limit"
	defaultGetRequestIdPattern   = "([a-z0-9]{42})"
)

type limitUsage struct {
	planLimit int64
	usage     int64
}

var userUsageLimit = map[string]limitUsage{}

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
	requestHandler.Ticking()
	return requestHandler, nil
}

func (a *RequestCrossoverLimiter) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	requestId := requestKey(a.requestIdPattern, req.URL.Path)
	parsedUUID, err := uuid.Parse(requestId[10:])
	if err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		// Write the error message to the response writer
		rw.Write([]byte("invalid requestId"))
		return
	}
	userId := parsedUUID.String()
	v, ok := userUsageLimit[userId]
	fmt.Println("userId:", userId)
	fmt.Println("Map:", userUsageLimit)
	if !ok {
		a.RateLimitPlan(userId)
	} else {
		if v.usage > v.planLimit {
			rw.WriteHeader(http.StatusTooManyRequests)
			rw.Write([]byte("too many requests"))
			return
		}
		v.usage++
		userUsageLimit[userId] = v
	}

	fmt.Println(userUsageLimit)
	req.Header.Set("X-UUId", uuid.NewString())
	a.next.ServeHTTP(rw, req)
}

func (a *RequestCrossoverLimiter) RateLimitPlan(userId string) error {
	httpReq, err := http.NewRequest(http.MethodGet, strings.Replace(a.rateLimitPlanLimitUrl, ":userId", userId, 1), nil)
	if err != nil {
		log.Printf("HTTPCALLERERRPlan: %s", err.Error())
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", a.apiKey)

	httpRes, err := a.client.Do(httpReq)
	fmt.Println("response,", httpRes)
	fmt.Println("err,", httpRes)
	if err != nil {
		log.Printf("HTTPDOERRPlan: %s", err.Error())
		return err
	}

	if httpRes.StatusCode != http.StatusOK {
		return err
	}
	fmt.Println("httpRes:......", httpReq.Body)

	body, err := ioutil.ReadAll(httpRes.Body)
	fmt.Println("body:......", body)

	if err != nil {
		log.Printf("PlanPasreBody: %s", err.Error())
		return err
	}

	var response map[string]map[string]int
	err = json.Unmarshal(body, &response)
	if err != nil {
		log.Printf("UNMARSHAERPlan: %s", err.Error())
		return err
	}

	//reset usage and limit
	userUsageLimit[userId] = limitUsage{
		usage:     0,
		planLimit: int64(response["data"]["request_limit"]),
	}

	return nil
}

func (a *RequestCrossoverLimiter) Ticking() {
	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for {
			fmt.Println("ticking..........")
			<-ticker.C
			for k, _ := range userUsageLimit {
				a.RateLimitPlan(k)
			}

		}
	}()
}
func requestKey(pattern string, path string) string {
	// Compile the regular expression
	re := regexp.MustCompile(pattern)
	// Find the first match of the pattern in the URL Path
	match := re.FindStringSubmatch(path)

	if len(match) == 0 {
		return ""
	}
	return match[0]
}
