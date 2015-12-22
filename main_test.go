package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/idada/frontd/aes256cbc"
	"github.com/idada/frontd/reuse"
	"golang.org/x/net/websocket"
)

var (
	_echoServerAddr      = []byte("127.0.0.1:62863")
	_blackHoleServerAddr = []byte("127.0.0.1:62864")
	_httpServerAddr      = []byte("127.0.0.1:62865")
	_websocketServerAddr = []byte("127.0.0.1:62866")
	_expectAESCiphertext = []byte("U2FsdGVkX19KIJ9OQJKT/yHGMrS+5SsBAAjetomptQ0=")
	_secret              = []byte("p0S8rX680*48")
	_defaultFrontdAddr   = "127.0.0.1:" + strconv.Itoa(_DefaultPort)
)

var (
	// use -reuse with go test enable SO_REUSEPORT
	// go test -parallel 6553 -benchtime 60s -bench BenchmarkEchoParallel -reuse
	// but it seems will not working with single backend addr because of
	// http://stackoverflow.com/questions/14388706/socket-options-so-reuseaddr-and-so-reuseport-how-do-they-differ-do-they-mean-t
	reuseTest = flag.Bool("reuse", false, "test reuseport dialer")
)

func TestMain(m *testing.M) {
	flag.Parse()
	if *reuseTest {
		fmt.Println("testing SO_REUSEPORT")
	}

	// start echo server
	go servEcho()

	// start frontd
	os.Setenv("SECRET", string(_secret))
	os.Setenv("BACKEND_TIMEOUT", "1")
	os.Setenv("MAX_HTTP_HEADER_SIZE", "1024")
	os.Setenv("PPROF_PORT", "62866")

	go main()

	// start http server
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
		if len(r.Header["X-Forwarded-For"]) > 0 {
			w.Write([]byte(r.Header["X-Forwarded-For"][0]))
		}
	})
	go http.ListenAndServe(string(_httpServerAddr), nil)

	// start webapp server
	http.Handle("/echo", websocket.Handler(func(ws *websocket.Conn) {
		io.Copy(ws, ws)
	}))
	go http.ListenAndServe(string(_websocketServerAddr), nil)

	rand.Seed(time.Now().UnixNano())

	// wait for servers to start
	time.Sleep(time.Second)
	os.Exit(m.Run())
}

func servEcho() {
	l, err := net.Listen("tcp", string(_echoServerAddr))
	if err != nil {
		fmt.Println("Error listening:", err.Error())
		os.Exit(1)
	}
	// Close the listener when the application closes.
	defer l.Close()
	fmt.Println("Listening on " + string(_echoServerAddr))
	for {
		// Listen for an incoming connection.
		c, err := l.Accept()
		if err != nil {
			fmt.Println("Error accepting: ", err.Error())
			os.Exit(1)
		}
		// Handle connections in a new goroutine.
		go func(c net.Conn) {
			defer c.Close()

			_, err := io.Copy(c, c)
			switch err {
			case io.EOF:
				err = nil
				return
			case nil:
				return
			}
			panic(err)
		}(c)
	}
}

// TestTextDecryptAES ---
func TestTextDecryptAES(t *testing.T) {
	dec, err := aes256cbc.DecryptBase64(_secret, _expectAESCiphertext)
	if err != nil {
		panic(err)
	}
	if !bytes.Equal(dec, _echoServerAddr) {
		panic(errors.New("not match"))
	}
}

// TestHTTPServer ---
func TestHTTPServer(t *testing.T) {
	cipherAddr, err := encryptText(_httpServerAddr, _secret)
	if err != nil {
		panic(err)
	}

	hdrs := map[string]string{
		string(_hdrCipherOrigin): string(cipherAddr),
		"X-Forwarded-For":        "8.8.8.8, 8.8.4.4",
	}

	testHTTPServer(hdrs, "OK127.0.0.1")

	testWebSocketServer(hdrs, "OK127.0.0.1")
}

func encryptText(plaintext, passphrase []byte) ([]byte, error) {
	return aes256cbc.EncryptBase64(passphrase, plaintext)
}

func testHTTPServer(hdrs map[string]string, expected string) {

	client := &http.Client{}
	req, _ := http.NewRequest("GET", "http://"+string(_defaultFrontdAddr), nil)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		panic(err)
	}

	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		panic(err)
	}

	if !bytes.HasPrefix(b, []byte("OK127.0.0.1")) {
		panic(fmt.Errorf("http reply not match: %s", string(b)))
	}
}

func testWebSocketServer(hdrs map[string]string, expected string) {
	origin := "http://127.0.0.1/"
	url := "ws://" + string(_defaultFrontdAddr) + "/echo"
	cfg, err := websocket.NewConfig(url, origin)
	if err != nil {
		panic(err)
	}

	for k, v := range hdrs {
		cfg.Header.Set(k, v)
	}

	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		panic(err)
	}
	if _, err := ws.Write([]byte(expected)); err != nil {
		panic(err)
	}
	var msg = make([]byte, len(expected))
	var n int
	if n, err = ws.Read(msg); err != nil {
		panic(err)
	}

	if expected != string(msg[:n]) {
		log.Println(string(msg[:n]))
		log.Println(expected)
		panic(fmt.Errorf("websocket reply not match: %s", string(msg[:n])))
	}

}

// TestEchoServer ---
func TestEchoServer(t *testing.T) {
	var conn net.Conn
	var err error
	if *reuseTest {
		conn, err = reuseport.Dial("tcp", "127.0.0.1:0", string(_echoServerAddr))
	} else {
		conn, err = dialTimeout("tcp", string(_echoServerAddr), time.Second*time.Duration(_BackendDialTimeout))
	}
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	n := rand.Int() % 10
	for i := 0; i < n; i++ {
		testEchoRound(conn)
	}
}

func testEchoRound(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(time.Second * 10))

	n := rand.Int()%2048 + 10
	out := randomBytes(n)
	n0, err := conn.Write(out)
	if err != nil {
		panic(err)
	}

	rcv := make([]byte, n)
	n1, err := io.ReadFull(conn, rcv)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if !bytes.Equal(out[:n0], rcv[:n1]) {
		fmt.Println("out: ", n0, "in:", n1)

		fmt.Println("out: ", hex.EncodeToString(out), "in:", hex.EncodeToString(rcv))
		panic(errors.New("echo server reply is not match"))
	}
}

func randomBytes(n int) []byte {

	b := make([]byte, n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i := 0; i < n; i++ {
		b[i] = byte(rand.Int())
	}

	return b
}

// TestProtocolDecrypt ---
func TestProtocolDecrypt(*testing.T) {
	b, err := encryptText(_echoServerAddr, _secret)
	if err != nil {
		panic(err)
	}
	testProtocol(append(b, '\n'), nil)

	// test cached hitted
	testProtocol(append(b, '\n'), nil)
}

func testProtocol(cipherAddr, expected []byte) {
	// * test decryption
	var conn net.Conn
	var err error
	if *reuseTest {
		conn, err = reuseport.Dial("tcp", "127.0.0.1:0", _defaultFrontdAddr)
	} else {
		conn, err = dialTimeout("tcp", _defaultFrontdAddr, time.Second*time.Duration(_BackendDialTimeout))
	}

	if err != nil {
		panic(err)
	}
	defer conn.Close()

	_, err = conn.Write(cipherAddr)
	if err != nil {
		panic(err)
	}

	if expected != nil {
		buf := make([]byte, len(expected))
		n, err := io.ReadFull(conn, buf)
		if err != nil && err != io.EOF {
			panic(err)
		}
		if !bytes.Equal(expected, buf[:n]) {
			fmt.Println(buf[:n])
			fmt.Println(string(buf[:n]))
			fmt.Println(string(expected))
			panic("expected reply not matched")
		}
		return
	}

	for i := 0; i < 5; i++ {
		testEchoRound(conn)
	}
}

// TestBinaryProtocolDecrypt ---
func TestBinaryProtocolDecrypt(*testing.T) {
	b, err := aes256cbc.Encrypt(_secret, _echoServerAddr)
	if err != nil {
		panic(err)
	}
	testProtocol(append(append([]byte{0}, byte(len(b))), b...), nil)
}

func TestBackendError(*testing.T) {
	b, err := encryptText(_blackHoleServerAddr, _secret)
	if err != nil {
		panic(err)
	}
	testProtocol(append(b, '\n'), []byte("4102"))
}

func TestBackendBinEmptyCipherReadErr(*testing.T) {
	testProtocol([]byte{0, 0}, []byte("4103"))
}

func TestBackendBinCipherDecryptErr(*testing.T) {
	testProtocol([]byte{0, 1, 3}, []byte("4106"))
}

func TestDecryptError(*testing.T) {
	testProtocol(append([]byte("2hws28"), '\n'), []byte("4106"))

	testProtocol(append([]byte("MjF3MjE="), '\n'), []byte("4106"))

	testProtocol(append([]byte("MjF3MjFldWhmMjh1ZTRoMjhoMzJlZDAzdzIwOWUzOTAyZWZqY2Vpd2hudmNpdXJoZXZ1aWllaGY4MjExOXZma25p6IOh5qOuMjF3MjFldWhmMjh1ZTRoMjhoMzJlZDAzdzIwOWUzOTAyZWZqY2Vpd2hudmNpdXJoZXZ1aWllaGY4MjExOXZma25p6IOh5qOuMjF3MjFldWhmMjh1ZTRoMjhoMzJlZDAzdzIwOWUzOTAyZWZqY2Vpd2hudmNpdXJoZXZ1aWllaGY4MjExOXZma25p6IOh5qOuMjF3MjFldWhmMjh1ZTRoMjhoMzJlZDAzdzIwOWUzOTAyZWZqY2Vpd2hudmNpdXJoZXZ1aWllaGY4MjExOXZma25p6IOh5qOuDQoNCjIxdzIxZXVoZjI4dWU0aDI4aDMyZWQwM3cyMDllMzkwMmVmamNlaXdobnZjaXVyaGV2dWlpZWhmODIxMTl2ZmtuaeiDoeajrjIxdzIxZXVoZjI4dWU0aDI4aDMyZWQwM3cyMDllMzkwMmVmamNlaXdobnZjaXVyaGV2dWlpZWhmODIxMTl2ZmtuaeiDoeajrjIxdzIxZXVoZjI4dWU0aDI4aDMyZWQwM3cyMDllMzkwMmVmamNlaXdobnZjaXVyaGV2dWlpZWhmODIxMTl2ZmtuaeiDoeajrg0KDQoyMXcyMWV1aGYyOHVlNGgyOGgzMmVkMDN3MjA5ZTM5MDJlZmpjZWl3aG52Y2l1cmhldnVpaWVoZjgyMTE5dmZrbmnog6Hmo64"), '\n'),
		[]byte("4106"))
}

func TestBackendTimeout(*testing.T) {
	b, err := encryptText([]byte("8.8.8.8:80"), _secret)
	if err != nil {
		panic(err)
	}
	testProtocol(append(b, '\n'), []byte("4101"))
}

// TODO: test error 0x07 - 0x10

// TODO: more test with and with out x-forwarded-for

// TODO: test decryption with extra bytes in packet and check data

// TODO: test decryption with seperated packet simulate loss connection and check data

// benchmarks

// TODO: benchmark 100, 1000 connect with 1k 10k 100k 1m data

func BenchmarkEncryptText(b *testing.B) {
	s1 := randomBytes(255)
	s2 := randomBytes(32)
	for i := 0; i < b.N; i++ {
		_, err := encryptText(s1, s2)
		if err != nil {
			panic(err)
		}
	}
}

func BenchmarkDecryptText(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := aes256cbc.DecryptBase64(_secret, _expectAESCiphertext)
		if err != nil {
			panic(err)
		}
	}
}

func BenchmarkEcho(b *testing.B) {
	for i := 0; i < b.N; i++ {
		TestEchoServer(&testing.T{})
	}
}

func BenchmarkLatency(b *testing.B) {
	cipherAddr, err := encryptText(_echoServerAddr, _secret)
	if err != nil {
		panic(err)
	}

	for i := 0; i < b.N; i++ {
		testProtocol(append(cipherAddr, '\n'), nil)
	}
}

func BenchmarkNoHitLatency(b *testing.B) {
	for i := 0; i < b.N; i++ {
		TestProtocolDecrypt(&testing.T{})
	}
}

func BenchmarkEchoParallel(b *testing.B) {

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			TestEchoServer(&testing.T{})
		}
	})
}

func BenchmarkLatencyParallel(b *testing.B) {
	cipherAddr, err := encryptText(_echoServerAddr, _secret)
	if err != nil {
		panic(err)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			testProtocol(append(cipherAddr, '\n'), nil)
		}
	})
}

func BenchmarkNoHitLatencyParallel(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			TestProtocolDecrypt(&testing.T{})
		}
	})
}

// with echo server with random hanging
// * benchmark latency
// * benchmark throughput
// * benchmark copy-on-write performance BackendAddrCache
// * benchmark memory footprint
