[![Build Status](https://travis-ci.org/xindong/frontd.svg?branch=master)](https://travis-ci.org/xindong/frontd)
[![Coverage Status](https://coveralls.io/repos/xindong/frontd/badge.svg?branch=master&service=github)](https://coveralls.io/github/xindong/frontd?branch=master)

### 简介

根据心动开放技术文档 [统一的Gateway](https://github.com/xindong/docs/blob/master/public/game_review/backend.md) 中的要求：
整体架构对公网可以仅暴露1个网关入口，即 1个IP 1个端口。
并要求这个网关在需要时可以水平扩展。
因此需要游戏开发和使用网关来与服务器后端进行通讯。

本项目提供一种通用方案，可以直接部署生产环境，也可以进行改造后部署。

### 主要特点

* 高性能、高并发
* 无状态，可水平扩展
* 安全（无明文后端地址及端口信息）
	* 后端地址端口使用AES（aes-256-cbc）加密
* 免配置免维护
	* 无论后端地址端口发生什么变化，本网关并不需要重新配置或维护
* 可使用容器部署 [官方镜像 https://hub.docker.com/r/tomasen/frontd/ ](https://hub.docker.com/r/tomasen/frontd/)

### 注意事项

切勿将 Secret Passphrase 写入客户端代码！

* Secret Passphrase 用来生成加密的地址信息
* 加密后的地址信息密文可以存放在客户端或通过其他方式发送给客户端，但切忌将 Secret Passphrase 写入客户端代码！

### 编译

`go build` 或 `docker build`


### 部署服务端

1. 通过环境变量 `SECRET` 设置解密用的秘钥
2. 可以使用官方 Docker 镜像 `tomasen/frontd`

	启动命令范例如下：

	`docker run -e "SECRET=SomePassphrase" tomasen/frontd /go/bin/frontd`


### 通讯协议

客户端建立TCP连接后，以文本形式发送 加密并的后端地址端口信息 + `\n` 换行符。之后开始正常通讯即可。

如果出现后端地址端口无法连接等错误，会根据下表返回二进制错误码：

| 错误码 | 含义 |
| --- | --- |
| 0x01   | 后端服务器超时 |
| 0x02   | 无法连接后端服务器 |
| 0x04   | 获取后端地址密文失败 |
| 0x06   | 后端地址解密失败 |
| 0x10   | 不被允许的IP地址 |


### 接入方式

1. 生成 Passphrase 。并保存在安全的文档中。
	 * 可以使用在线生成 https://lastpass.com/generatepassword.php
2. 使用上述 Secret Passphrase 部署服务端
3. 使用 AES 算法加密文本格式的后端地址，生成 base64 编码的密文。可以使用在线工具如 [http://tool.oschina.net/encrypt] 生成密文 。也可以使用 `openssl` 命令行如 `echo -n "127.0.0.1:62863" | openssl enc -e -aes-256-cbc -a -salt -k "p0S8rX680*48"` 生成密文。
	* 例：当后端地址为 `127.0.0.1:62863` 时，如 Passphrase=p0S8rX680*48 ，
	密文结果应类似 `U2FsdGVkX19KIJ9OQJKT/yHGMrS+5SsBAAjetomptQ0=` <br/>
	_注：上述方式都会使用随机Salt——这也是建议的方式。其结果是每次加密得出的密文结果并不一样，但并不会影响解密_
4. 客户端与网关建立连接后，将后端地址的密文文本加一个换行符发送给网关。建立连接。
	* 根据前例： 应该发送 `U2FsdGVkX19KIJ9OQJKT/yHGMrS+5SsBAAjetomptQ0=\n`

### Benchmark 基准测试数据指标

* 测试环境

	Mac Mini(i7, 16G ram)

* 测试数据

 	当地址缓存命中时（正常情形） `frontd` 为后端带来的额外延迟为 0.57ms <br/>
	当地址缓存完全无命时（极端情况）  `frontd` 为后端带来的额外延迟为 1.05ms

	```bash
BenchmarkEncryptText-4 	  200000	      7345 ns/op
BenchmarkDecryptText-4 	  500000	      4279 ns/op
BenchmarkEcho-4        	    1000	   1151079 ns/op
BenchmarkLatency-4     	    1000	   1727021 ns/op
BenchmarkNoHitLatency-4	    1000	   2205162 ns/op
	```

* 测试方法

	`go test -bench .`

### Profiling

如果启动时通过环境变量 `PPROF_PORT`，就会在该端口启动 pprof 。使用方法可以参考 [https://golang.org/pkg/net/http/pprof/]

	启动命令范例如下：

	`docker run -e "SECRET=SomePassphrase" -e "PPROF_PORT=4044" -p 4044 tomasen/frontd /go/bin/frontd`

### 设计说明

`frontd` 在设计上是安全性+性能+易于接入+易于维护的折中方案。其中：

* 使用加密地址，而不是预配置地址表，是出于易于维护的角度
* 使用AES是安全性考虑，防止攻击者篡改变造后端目标地址
* 使用文本而不是二进制通讯，是方便客户端和服务器列表部分功能的接入

折中意味着牺牲。因此，我们并不反对项目组在本代码基础上进行优化，但不建议弱化安全性的部分。

### Developing

Pull request must pass:

* [golint](https://github.com/golang/lint)
* [go vet](https://godoc.org/golang.org/x/tools/cmd/vet)
* [gofmt](https://golang.org/cmd/gofmt)
* [go test](https://golang.org/cmd/go/#hdr-Test_packages)

### TODO

- [ ] Improve test coverage to over 90%
- [ ] 支持 net/http/pprof
- [ ] 支持 HTTP 反向代理 （使用 Header `X-AskForOrigin`）
- [ ] 支持 binary protocol
- [ ] 支持更多加密解密算法
- [ ] 支持 consul 服务发现
