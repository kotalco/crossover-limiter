package crossover_limiter

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

const DefaultTimeout = 5

type PlanProxyResponse struct {
	Data struct {
		RequestLimit int `json:"request_limit"`
	} `json:"data"`
}

type IPlanProxy interface {
	fetch(userId string) (int, error)
}

type PlanProxy struct {
	httpClient http.Client
	requestUrl *url.URL
	apiKey     string
}

func NewPlanProxy(apiKey string, rawUrl string) IPlanProxy {
	requestUrl, err := url.Parse(rawUrl)
	if err != nil {
		panic(fmt.Sprintf("invalid raw plan proxy url %s: %v", rawUrl, err))
	}
	return &PlanProxy{
		httpClient: http.Client{
			Timeout: DefaultTimeout * time.Second,
		},
		requestUrl: requestUrl,
		apiKey:     apiKey,
	}
}

func (proxy *PlanProxy) fetch(userId string) (int, error) {
	queryParams := url.Values{}
	queryParams.Set("userId", userId)
	proxy.requestUrl.RawQuery = queryParams.Encode()
	httpReq, err := http.NewRequest(http.MethodGet, proxy.requestUrl.String(), nil)
	if err != nil {
		log.Printf("FetchUserPlan:NewRequest, %s", err.Error())
		return 0, errors.New("something went wrong")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", proxy.apiKey)

	httpRes, err := proxy.httpClient.Do(httpReq)
	defer httpRes.Body.Close()
	if err != nil {
		log.Printf("FetchUserPlan:Do, %s", err.Error())
		return 0, errors.New("something went wrong")
	}

	if httpRes.StatusCode != http.StatusOK {
		log.Printf("FetchUserPlan:InvalidStatusCode: %d", httpRes.StatusCode)
		return 0, errors.New("something went wrong")
	}

	var response PlanProxyResponse
	if err = json.NewDecoder(httpRes.Body).Decode(&response); err != nil {
		log.Printf("FetchUserPlan:UNMARSHAERPlan, %s", err.Error())
		return 0, errors.New("something went wrong")
	}

	return response.Data.RequestLimit, nil
}
