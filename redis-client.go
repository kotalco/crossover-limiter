package crossover_limiter

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

type RedisClient struct {
	pool     chan *RedisConnection
	address  string
	poolSize int
	mu       sync.Mutex // protects pool from race condition
	auth     string
}

type RedisConnection struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

func NewRedisClient(address string, poolSize int, auth string) *RedisClient {
	client := &RedisClient{
		pool:     make(chan *RedisConnection, poolSize),
		address:  address,
		poolSize: poolSize,
		auth:     auth,
	}

	// pre-populate the pool with connections , authenticated and ready to be used
	for i := 0; i < poolSize; i++ {
		conn, err := NewRedisConnection(address, auth)
		if err != nil {
			log.Println(err.Error())
			continue
		}
		client.pool <- conn
	}

	return client
}

func NewRedisConnection(address string, auth string) (*RedisConnection, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}

	rc := &RedisConnection{
		conn: conn,
		rw:   bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)),
	}

	if auth != "" {
		// Authenticate with Redis using the AUTH command
		if err := rc.Auth(auth); err != nil {
			conn.Close()
			return nil, err
		}
	}

	return rc, nil
}

func (client *RedisClient) Do(command string) (string, error) {
	conn, err := client.GetConnection()
	if err != nil {
		return "", err
	}
	defer client.ReleaseConnection(conn)

	err = conn.Send(command)
	if err != nil {
		return "", err
	}

	reply, err := conn.Receive()
	if err != nil {
		return "", err
	}

	return reply, nil
}

func (client *RedisClient) Set(key string, value string) error {
	response, err := client.Do(fmt.Sprintf("*3\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(key), key, len(value), value))
	if err != nil {
		return err
	}
	if response != "+OK" {
		return errors.New("unexpected response from server")
	}
	return nil
}

func (client *RedisClient) Incr(key string) (int, error) {
	// Construct the Redis INCR command
	command := fmt.Sprintf("*2\r\n$4\r\nINCR\r\n$%d\r\n%s\r\n", len(key), key)

	// Send the command to the Redis server
	response, err := client.Do(command)
	if err != nil {
		return 0, err
	}

	// Parse the response => should be in the format: ":<number>\r\n" for a successful INCR command
	var newValue int
	if _, err := fmt.Sscanf(response, ":%d\r\n", &newValue); err != nil {
		return 0, errors.New("unexpected response from server")
	}

	// Return the new value
	return newValue, nil
}

func (client *RedisClient) Expire(key string, seconds int) (bool, error) {
	// Construct the Redis EXPIRE command
	command := fmt.Sprintf("*3\r\n$6\r\nEXPIRE\r\n$%d\r\n%s\r\n$%d\r\n%d\r\n", len(key), key, len(fmt.Sprintf("%d", seconds)), seconds)

	// Send the command to the Redis server
	response, err := client.Do(command)
	if err != nil {
		return false, err
	}

	// Parse the response => should be in the format: ":1" for a successful EXPIRE command (if the key exists), or ":0" if it does not.
	//notice that the response was in  ":1\r\n"  format then it was stripped from it's suffix in the do function
	if response == ":1" {
		return true, nil
	} else if response == ":0" {
		return false, nil
	} else {
		return false, errors.New("unexpected response from server")
	}
}

func (client *RedisClient) SetWithTTL(key string, value string, ttl int) error {
	response, err := client.Do(fmt.Sprintf("*5\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$2\r\nEX\r\n$%d\r\n%d\r\n", len(key), key, len(value), value, len(strconv.Itoa(ttl)), ttl))
	if err != nil {
		return err
	}
	if response != "+OK" {
		return errors.New("unexpected response from server: " + response)
	}
	return nil
}

func (client *RedisClient) Get(key string) (string, error) {
	response, err := client.Do(fmt.Sprintf("*2\r\n$3\r\nGET\r\n$%d\r\n%s\r\n", len(key), key))
	if err != nil {
		return "", err
	}
	return response, nil
}

func (client *RedisClient) Close() {
	close(client.pool)
	for conn := range client.pool {
		conn.Close()
	}
}

func (client *RedisClient) GetConnection() (*RedisConnection, error) {
	// make sure that the access to the client.pool is synchronized among concurrent goroutines, make the operation atomic to prevent race conditions
	client.mu.Lock()
	defer client.mu.Unlock()

	select {
	case conn := <-client.pool:
		return conn, nil
	default:
		// Pool is empty now all connection are being used , create a new connection till some connections get released
		conn, err := NewRedisConnection(client.address, client.auth)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

func (client *RedisClient) ReleaseConnection(conn *RedisConnection) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if len(client.pool) >= client.poolSize {
		conn.Close() //if the pool is full the new conn is closed and discarded
	} else {
		client.pool <- conn //if there is room put into the pool for future use

	}
}

func (rc *RedisConnection) Auth(password string) error {
	if err := rc.Send(fmt.Sprintf("AUTH %s", password)); err != nil {
		return err
	}
	reply, err := rc.Receive()
	if err != nil {
		return err
	}
	if reply != "+OK" {
		return errors.New("authentication failed")
	}
	return nil
}

func (rc *RedisConnection) Send(command string) error {
	_, err := rc.rw.WriteString(command + "\r\n")
	if err != nil {
		return err
	}
	return rc.rw.Flush()
}

func (rc *RedisConnection) Receive() (string, error) {
	line, err := rc.rw.ReadString('\n')
	if err != nil {
		return "", err
	}
	if line[0] == '-' { // if the response contains - then it's a simple errors
		return "", fmt.Errorf(strings.TrimSuffix(line[1:], "\r\n"))
	}
	//Assume the reply is a bulk string ,array serialization ain't supported in this client
	if line[0] == '$' {
		length, _ := strconv.Atoi(strings.TrimSuffix(line[1:], "\r\n")) //trim the CRLF from our response
		if length == -1 {
			// This is a nil reply
			return "", nil
		}
		buf := make([]byte, length+2) // +2 for the CRLF (\r\n)
		_, err = rc.rw.Read(buf)
		if err != nil {
			return "", err
		}
		return string(buf[:length]), nil
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func (rc *RedisConnection) Close() error {
	return rc.conn.Close()
}
