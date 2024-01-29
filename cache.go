package crossover_limiter

import (
	"bytes"
	"encoding/gob"
	"log"
	"net/http"
)

type CacheService struct {
	cacheExpiry int
	redisClient *RedisClient
	next        http.Handler
}
type CachedResponse struct {
	StatusCode int
	Headers    map[string][]string
	Body       []byte
}

func NewCacheService(cacheExpiry int, redisClient *RedisClient, next http.Handler) *CacheService {
	// This needs to be called once to register the type if using gob
	gob.Register(CachedResponse{})
	return &CacheService{
		cacheExpiry: cacheExpiry,
		redisClient: redisClient,
		next:        next,
	}
}

func (cache *CacheService) ServeHTTP(rw http.ResponseWriter, req *http.Request) {

	// cache key based on the request
	cacheKey := req.URL.Path

	// retrieve the cached response
	cachedData, err := cache.redisClient.Get(cacheKey)
	if err == nil && cachedData != "" {
		// Cache hit - parse the cached response and write it to the original ResponseWriter
		var cachedResponse CachedResponse
		buffer := bytes.NewBufferString(cachedData)
		dec := gob.NewDecoder(buffer)
		if err := dec.Decode(&cachedResponse); err == nil {
			for key, values := range cachedResponse.Headers {
				for _, value := range values {
					rw.Header().Add(key, value)
				}
			}
			rw.WriteHeader(cachedResponse.StatusCode)
			_, _ = rw.Write(cachedResponse.Body)
			return
		}
		log.Printf("Failed to serialize response for caching: %s", err.Error())
		_ = cache.redisClient.Delete(cacheKey)
	}

	// Cache miss - record the response
	recorder := &responseRecorder{rw: rw}
	cache.next.ServeHTTP(recorder, req)

	// Serialize the response data
	cachedResponse := CachedResponse{
		StatusCode: recorder.status,
		Headers:    recorder.Header().Clone(), // Convert http.Header to a map for serialization
		Body:       recorder.body.Bytes(),
	}
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	if err := enc.Encode(cachedResponse); err != nil {
		log.Printf("Failed to serialize response for caching: %s", err)
	}

	// Store the serialized response in Redis as a string with an expiration time
	if err := cache.redisClient.SetWithTTL(cacheKey, buffer.String(), cache.cacheExpiry); err != nil {
		log.Println("Failed to cache response in Redis:", err)
	}

	// Write the original response
	rw.WriteHeader(recorder.status)
	_, _ = rw.Write(recorder.body.Bytes())
	return
}
