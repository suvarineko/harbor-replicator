package client

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"harbor-replicator/pkg/config"
)

type TransportConfig struct {
	Timeout         time.Duration
	MaxIdleConns    int
	MaxConnsPerHost int
	IdleConnTimeout time.Duration
	TLSConfig       *TLSTransportConfig
	KeepAlive       time.Duration
	ResponseHeaderTimeout time.Duration
	ExpectContinueTimeout time.Duration
}

type TLSTransportConfig struct {
	InsecureSkipVerify bool
	CertFile           string
	KeyFile            string
	CAFile             string
	MinVersion         uint16
	MaxVersion         uint16
	CipherSuites       []uint16
}

func NewTransport(cfg *config.HarborInstanceConfig) (*Transport, error) {
	transportConfig := &TransportConfig{
		Timeout:         cfg.Timeout,
		MaxIdleConns:    100,
		MaxConnsPerHost: 10,
		IdleConnTimeout: 90 * time.Second,
		KeepAlive:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	if cfg.Insecure {
		transportConfig.TLSConfig = &TLSTransportConfig{
			InsecureSkipVerify: true,
		}
	}

	baseTransport, err := createHTTPTransport(transportConfig)
	if err != nil {
		return nil, err
	}

	transport := &Transport{
		baseTransport:   baseTransport,
		timeout:         transportConfig.Timeout,
		maxIdleConns:    transportConfig.MaxIdleConns,
		maxConnsPerHost: transportConfig.MaxConnsPerHost,
	}

	return transport, nil
}

func createHTTPTransport(cfg *TransportConfig) (http.RoundTripper, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: cfg.KeepAlive,
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: cfg.ExpectContinueTimeout,
		ForceAttemptHTTP2:     true,
	}

	if cfg.TLSConfig != nil {
		tlsConfig, err := createTLSConfig(cfg.TLSConfig)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = tlsConfig
	}

	return transport, nil
}

func createTLSConfig(cfg *TLSTransportConfig) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		MinVersion:         cfg.MinVersion,
		MaxVersion:         cfg.MaxVersion,
	}

	if cfg.MinVersion == 0 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}

	if len(cfg.CipherSuites) > 0 {
		tlsConfig.CipherSuites = cfg.CipherSuites
	}

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.baseTransport.RoundTrip(req)
}

func (t *Transport) CloseIdleConnections() {
	if transport, ok := t.baseTransport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (t *Transport) GetStats() TransportStats {
	stats := TransportStats{
		MaxIdleConns:    t.maxIdleConns,
		MaxConnsPerHost: t.maxConnsPerHost,
		Timeout:         t.timeout,
	}

	if transport, ok := t.baseTransport.(*http.Transport); ok {
		stats.IdleConnections = countIdleConnections(transport)
	}

	return stats
}

type TransportStats struct {
	MaxIdleConns     int
	MaxConnsPerHost  int
	IdleConnections  int
	Timeout          time.Duration
}

func countIdleConnections(transport *http.Transport) int {
	return 0
}

type PooledTransport struct {
	*Transport
	pool           *ConnectionPool
	poolConfig     *PoolConfig
}

type ConnectionPool struct {
	maxSize        int
	currentSize    int
	idleTimeout    time.Duration
	cleanupInterval time.Duration
	connections    map[string]*PooledConnection
	stopCleanup    chan bool
}

type PooledConnection struct {
	conn       net.Conn
	lastUsed   time.Time
	inUse      bool
	remoteAddr string
}

type PoolConfig struct {
	MaxSize         int
	IdleTimeout     time.Duration
	CleanupInterval time.Duration
}

func NewPooledTransport(cfg *config.HarborInstanceConfig, poolConfig *PoolConfig) (*PooledTransport, error) {
	transport, err := NewTransport(cfg)
	if err != nil {
		return nil, err
	}

	if poolConfig == nil {
		poolConfig = &PoolConfig{
			MaxSize:         50,
			IdleTimeout:     5 * time.Minute,
			CleanupInterval: 1 * time.Minute,
		}
	}

	pool := &ConnectionPool{
		maxSize:         poolConfig.MaxSize,
		idleTimeout:     poolConfig.IdleTimeout,
		cleanupInterval: poolConfig.CleanupInterval,
		connections:     make(map[string]*PooledConnection),
		stopCleanup:     make(chan bool),
	}

	pooledTransport := &PooledTransport{
		Transport:  transport,
		pool:       pool,
		poolConfig: poolConfig,
	}

	go pooledTransport.pool.startCleanupRoutine()

	return pooledTransport, nil
}

func (pt *PooledTransport) GetPoolStats() PoolStats {
	return PoolStats{
		MaxSize:        pt.pool.maxSize,
		CurrentSize:    pt.pool.currentSize,
		IdleTimeout:    pt.pool.idleTimeout,
		ActiveConns:    pt.getActiveConnections(),
		IdleConns:      pt.getIdleConnections(),
	}
}

func (pt *PooledTransport) getActiveConnections() int {
	count := 0
	for _, conn := range pt.pool.connections {
		if conn.inUse {
			count++
		}
	}
	return count
}

func (pt *PooledTransport) getIdleConnections() int {
	count := 0
	for _, conn := range pt.pool.connections {
		if !conn.inUse {
			count++
		}
	}
	return count
}

func (pt *PooledTransport) Close() error {
	close(pt.pool.stopCleanup)
	
	for _, conn := range pt.pool.connections {
		if conn.conn != nil {
			conn.conn.Close()
		}
	}
	
	pt.pool.connections = make(map[string]*PooledConnection)
	pt.pool.currentSize = 0
	
	return nil
}

func (cp *ConnectionPool) startCleanupRoutine() {
	ticker := time.NewTicker(cp.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cp.cleanupIdleConnections()
		case <-cp.stopCleanup:
			return
		}
	}
}

func (cp *ConnectionPool) cleanupIdleConnections() {
	now := time.Now()
	
	for addr, conn := range cp.connections {
		if !conn.inUse && now.Sub(conn.lastUsed) > cp.idleTimeout {
			if conn.conn != nil {
				conn.conn.Close()
			}
			delete(cp.connections, addr)
			cp.currentSize--
		}
	}
}

type PoolStats struct {
	MaxSize     int
	CurrentSize int
	IdleTimeout time.Duration
	ActiveConns int
	IdleConns   int
}

type ConnectionMetrics struct {
	TotalConnections     int64
	ActiveConnections    int64
	IdleConnections      int64
	ConnectionsCreated   int64
	ConnectionsDestroyed int64
	ConnectionErrors     int64
	AverageConnTime      time.Duration
}

func (pt *PooledTransport) GetConnectionMetrics() ConnectionMetrics {
	stats := pt.GetPoolStats()
	
	return ConnectionMetrics{
		TotalConnections:  int64(stats.CurrentSize),
		ActiveConnections: int64(stats.ActiveConns),
		IdleConnections:   int64(stats.IdleConns),
	}
}