package service

import (
	"regexp"
	"testing"
)

// 设备身份的形态：位数、十六进制、密钥可解析、Proof 可自校验。
func TestTraeDevice(t *testing.T) {
	for _, platform := range []string{PlatformTraeCN, PlatformTraeIntl} {
		site := traeSiteForPlatform(platform)
		dev, err := newTraeDevice(site)
		if err != nil {
			t.Fatalf("%s: 生成设备失败: %v", platform, err)
		}

		if len(dev.DeviceID) != site.DeviceIDDigits {
			t.Errorf("%s: device_id 长度应为 %d，实际 %d", platform, site.DeviceIDDigits, len(dev.DeviceID))
		}
		if !regexp.MustCompile(`^[0-9]+$`).MatchString(dev.DeviceID) {
			t.Errorf("%s: device_id 应全为数字，实际 %q", platform, dev.DeviceID)
		}
		if dev.DeviceID[0] == '0' {
			t.Errorf("%s: device_id 不应有前导 0，实际 %q", platform, dev.DeviceID)
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(dev.MachineID) {
			t.Errorf("%s: machine_id 应为 32 位十六进制，实际 %q", platform, dev.MachineID)
		}
		if _, err := dev.parsePrivateKey(); err != nil {
			t.Errorf("%s: 私钥不可解析: %v", platform, err)
		}
		if _, err := dev.parsePublicKey(); err != nil {
			t.Errorf("%s: 公钥不可解析: %v", platform, err)
		}
		// 同一账号再取一次设备，id 必须变化（每次登录都是新设备）。
		again, err := newTraeDevice(site)
		if err != nil {
			t.Fatalf("%s: 再次生成设备失败: %v", platform, err)
		}
		if again.DeviceID == dev.DeviceID {
			t.Errorf("%s: 两次生成的 device_id 不应相同", platform)
		}
	}
}

// DeviceProof 的签名能被自己的公钥验证，且签名原文的顺序与 Trae 要求一致。
func TestTraeDeviceProof(t *testing.T) {
	site := traeSiteForPlatform(PlatformTraeCN)
	dev, err := newTraeDevice(site)
	if err != nil {
		t.Fatalf("生成设备失败: %v", err)
	}

	const (
		method       = "POST"
		path         = "/trae/api/v3/oauth/ExchangeToken"
		clientID     = traeChatClientID
		refreshToken = "refresh-token-value"
		timestamp    = int64(1794854328)
		nonce        = "nonce-value"
	)

	proof, err := dev.signTraeDeviceProof(method, path, clientID, refreshToken, timestamp, nonce)
	if err != nil {
		t.Fatalf("签 DeviceProof 失败: %v", err)
	}
	ok, err := dev.verifyTraeDeviceProof(method, path, clientID, refreshToken, timestamp, nonce, proof)
	if err != nil {
		t.Fatalf("校验 DeviceProof 失败: %v", err)
	}
	if !ok {
		t.Fatal("DeviceProof 自校验未通过")
	}

	// 任一要素被改动都应校验失败。
	ok, err = dev.verifyTraeDeviceProof(method, path, clientID, refreshToken, timestamp+1, nonce, proof)
	if err != nil {
		t.Fatalf("校验 DeviceProof 失败: %v", err)
	}
	if ok {
		t.Error("时间戳变化后 Proof 不应仍然有效")
	}

	// 签名原文：六行，顺序为 方法 / 路径 / 客户端 / refresh / 时间戳 / 随机串。
	want := "POST\n/trae/api/v3/oauth/ExchangeToken\n" + clientID + "\n" + refreshToken + "\n1794854328\n" + nonce
	if got := traeDeviceProofPayload(method, path, clientID, refreshToken, timestamp, nonce); got != want {
		t.Errorf("签名原文不符:\n got=%q\nwant=%q", got, want)
	}
	// 方法大小写不敏感。
	if got := traeDeviceProofPayload("post", path, clientID, refreshToken, timestamp, nonce); got != want {
		t.Errorf("方法应统一大写:\n got=%q\nwant=%q", got, want)
	}
}
