package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Trae 的设备身份。
//
// 与 WorkBuddy 的「硬件指纹」是同一个角色，但字段与用途不同：Trae 在登录时把
// 设备（device_id / machine_id / ECDSA 公钥）交给授权页，之后每个对话请求都要
// 带上同一套设备，续期时还要用它签 DeviceProof。设备一旦每次变化，上游会直接
// 丢弃请求（社区插件的实测结论），所以设备必须随账号持久化。
type traeDevice struct {
	// DeviceID 是设备的对外 id：国内站 16 位数字，国际站 19 位数字。
	DeviceID string `json:"device_id"`
	// MachineID 是 32 位十六进制的机器码。
	MachineID string `json:"machine_id"`
	// PrivateKeyPEM 是 ECDSA P-256 私钥（PKCS#8 PEM），用于签 DeviceProof。
	PrivateKeyPEM string `json:"private_key_pem"`
	// PublicKeyPEM 是对应公钥，登录时需要交给授权页。
	PublicKeyPEM string `json:"public_key_pem"`
	// DeviceBrand / DeviceType / OSVersion 构成设备画像。
	DeviceBrand string `json:"device_brand"`
	DeviceType  string `json:"device_type"`
	OSVersion   string `json:"os_version"`
}

// newTraeDevice 为一个账号生成设备身份。
//
// digits 取站点配置的 DeviceIDDigits：国内站 16 位、国际站 19 位。
func newTraeDevice(site *traeSite) (*traeDevice, error) {
	if site == nil {
		return nil, errors.New("trae: site 不能为空")
	}
	deviceID, err := newTraeDeviceID(site.DeviceIDDigits)
	if err != nil {
		return nil, err
	}
	machineID, err := newTraeMachineID()
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("trae: 生成设备密钥失败: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("trae: 序列化设备私钥失败: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("trae: 序列化设备公钥失败: %w", err)
	}
	return &traeDevice{
		DeviceID:      deviceID,
		MachineID:     machineID,
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})),
		PublicKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		DeviceBrand:   site.Brand,
		DeviceType:    "windows",
		OSVersion:     "Windows 10",
	}, nil
}

// newTraeDeviceID 生成指定长度的纯数字设备 id，首位非 0。
func newTraeDeviceID(digits int) (string, error) {
	if digits <= 0 {
		return "", fmt.Errorf("trae: device_id 长度非法: %d", digits)
	}
	var b strings.Builder
	b.Grow(digits)
	for i := 0; i < digits; i++ {
		max := 10
		if i == 0 {
			max = 9 // 首位 1..9，避免出现前导 0
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
		if err != nil {
			return "", err
		}
		offset := int64(0)
		if i == 0 {
			offset = 1
		}
		b.WriteString(fmt.Sprintf("%d", n.Int64()+offset))
	}
	return b.String(), nil
}

// newTraeMachineID 生成 32 位十六进制机器码。
func newTraeMachineID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// traeDeviceProofPayload 是 DeviceProof 的签名原文。
//
// 与 Trae 客户端一致：方法、路径、客户端 id、refresh token、时间戳、随机串
// 以换行连接，缺一项或顺序不同都会被上游拒绝。
func traeDeviceProofPayload(method, path, clientID, refreshToken string, timestamp int64, nonce string) string {
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%d\n%s",
		strings.ToUpper(strings.TrimSpace(method)),
		path,
		clientID,
		refreshToken,
		timestamp,
		nonce,
	)
}

// signTraeDeviceProof 用设备私钥对签名原文做 ECDSA-SHA256，返回 base64(DER)。
func (d *traeDevice) signTraeDeviceProof(method, path, clientID, refreshToken string, timestamp int64, nonce string) (string, error) {
	if d == nil {
		return "", errors.New("trae: 设备为空")
	}
	key, err := d.parsePrivateKey()
	if err != nil {
		return "", err
	}
	payload := traeDeviceProofPayload(method, path, clientID, refreshToken, timestamp, nonce)
	digest := sha256.Sum256([]byte(payload))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("trae: 签 DeviceProof 失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// parsePrivateKey 解析设备私钥。
func (d *traeDevice) parsePrivateKey() (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(d.PrivateKeyPEM))
	if block == nil {
		return nil, errors.New("trae: 设备私钥不是 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("trae: 解析设备私钥失败: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("trae: 设备私钥不是 ECDSA")
	}
	return key, nil
}

// parsePublicKey 解析设备公钥（校验签名与登录时使用）。
func (d *traeDevice) parsePublicKey() (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(d.PublicKeyPEM))
	if block == nil {
		return nil, errors.New("trae: 设备公钥不是 PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("trae: 解析设备公钥失败: %w", err)
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("trae: 设备公钥不是 ECDSA")
	}
	return key, nil
}

// verifyTraeDeviceProof 校验一个 DeviceProof（供测试与自检使用）。
func (d *traeDevice) verifyTraeDeviceProof(method, path, clientID, refreshToken string, timestamp int64, nonce, proof string) (bool, error) {
	pub, err := d.parsePublicKey()
	if err != nil {
		return false, err
	}
	raw, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		return false, err
	}
	payload := traeDeviceProofPayload(method, path, clientID, refreshToken, timestamp, nonce)
	digest := sha256.Sum256([]byte(payload))
	return ecdsa.VerifyASN1(pub, digest[:], raw), nil
}
