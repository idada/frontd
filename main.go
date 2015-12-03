package main

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xindong/frontd/aes256cbc"
)

const (
	// max open file should at least be
	_MaxOpenfile              = uint64(1024 * 1024 * 1024)
	_MaxBackendAddrCacheCount = 1024 * 1024
	_DefaultPort              = 4043
	_MTU                      = 1500
)

var (
	_SecretPassphase []byte
	_Aes256CBC       = aes256cbc.New()
)

var (
	_BackendAddrCacheMutex sync.Mutex
	_BackendAddrCache      atomic.Value
	_BufioReaderPool       sync.Pool
)

type backendAddrMap map[string][]byte

func init() {
	_BackendAddrCache.Store(make(backendAddrMap))
}

func decryptBackendAddr(line []byte) ([]byte, error) {
	// Try to check cache
	m1 := _BackendAddrCache.Load().(backendAddrMap)
	addr, ok := m1[string(line)]
	if ok {
		return addr, nil
	}
	// Try to decrypt it (AES)
	addr, err := _Aes256CBC.Decrypt(_SecretPassphase, line)
	if err != nil {
		return nil, err
	}

	cacheBackendAddr(string(line), addr)
	return addr, nil
}

func cacheBackendAddr(key string, val []byte) {
	_BackendAddrCacheMutex.Lock()
	defer _BackendAddrCacheMutex.Unlock()

	m1 := _BackendAddrCache.Load().(backendAddrMap)
	// double check
	if _, ok := m1[key]; ok {
		return
	}

	m2 := make(backendAddrMap)
	// flush cache if there is way too many
	if len(m1) < _MaxBackendAddrCacheCount {
		// copy-on-write
		for k, v := range m1 {
			m2[k] = v // copy all data from the current object to the new one
		}
	}
	m2[key] = val
	_BackendAddrCache.Store(m2) // atomically replace the current object with the new one
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	os.Setenv("GOTRACEBACK", "crash")

	var lim syscall.Rlimit
	syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim)
	if lim.Cur < _MaxOpenfile || lim.Max < _MaxOpenfile {
		lim.Cur = _MaxOpenfile
		lim.Max = _MaxOpenfile
		syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim)
	}

	_SecretPassphase = []byte(os.Getenv("SECRET"))

	pprofPort, err := strconv.Atoi(os.Getenv("PPROF_PORT"))
	if err == nil && pprofPort > 0 && pprofPort <= 65535 {
		go func() {
			log.Println(http.ListenAndServe(":"+strconv.Itoa(pprofPort), nil))
		}()
	}

	listenAndServe()
}

func listenAndServe() {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(_DefaultPort))
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	var tempDelay time.Duration
	for {
		conn, err := l.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				time.Sleep(tempDelay)
				continue
			}
			log.Fatal(err)
		}
		tempDelay = 0
		go handleConn(conn)
	}
}

func handleConn(c net.Conn) {
	defer func() {
		c.Close()
		if r := recover(); r != nil {
			log.Println("Recovered in", r, ":", string(debug.Stack()))
		}
	}()

	// TODO: get rid of bufio.Reader
	// TODO: use binary protocol if first byte is 0x00

	// Read first line
	rdr, ok := _BufioReaderPool.Get().(*bufio.Reader)
	if ok {
		rdr.Reset(c)
	} else {
		rdr = bufio.NewReader(c)
	}
	defer _BufioReaderPool.Put(rdr)

	line, isPrefix, err := rdr.ReadLine()
	if err != nil || isPrefix {
		log.Println(err)
		c.Write([]byte{0x04})
		return
	}

	// Try to check cache
	addr, err := decryptBackendAddr(line)
	if err != nil {
		c.Write([]byte{0x06})
		return
	}

	// TODO: check if addr is allowed

	// Build tunnel
	backend, err := net.Dial("tcp", string(addr))
	if err != nil {
		// handle error
		switch err := err.(type) {
		case net.Error:
			if err.Timeout() {
				c.Write([]byte{0x01})
				log.Println(err)
				return
			}
		}
		log.Println(err)
		c.Write([]byte{0x02})
		return
	}
	defer backend.Close()

	// Start transfering data
	go pipe(c, backend)
	pipe(backend, rdr)
}

// pipe upstream and downstream
func pipe(dst io.Writer, src io.Reader) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recovered in", r, ":", string(debug.Stack()))
		}
	}()

	_, err := io.Copy(dst, src)

	switch err {
	case io.EOF:
		err = nil
		return
	case nil:
		return
	}
	// log.Println("pipe:", n, err)
}
