package webauth

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const (
	credentialVersion     = 1
	credentialAlgorithm   = "PBKDF2-HMAC-SHA256"
	credentialIterations  = 600_000
	credentialSaltBytes   = 16
	credentialDigestBytes = 32
	credentialFileMaxSize = 4 << 10
	minimumPasswordBytes  = 15
	maximumPasswordBytes  = 1024
)

// Credentials holds the derived administrator credential material.
type Credentials struct {
	salt   []byte
	digest []byte
}

type credentialFile struct {
	Version    int    `json:"version"`
	Username   string `json:"username"`
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Digest     string `json:"digest"`
}

// LoadCredentials reads and strictly validates a credential store.
func LoadCredentials(path string) (Credentials, error) {
	file, err := openCredentialFile(path)
	if err != nil {
		return Credentials{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, credentialFileMaxSize+1))
	if err != nil {
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	if len(data) > credentialFileMaxSize {
		return Credentials{}, errors.New("credential file is too large")
	}

	var stored credentialFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return Credentials{}, fmt.Errorf("decode credentials: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Credentials{}, errors.New("credential file has trailing data")
	}
	if stored.Version != credentialVersion || stored.Username != Username ||
		stored.Algorithm != credentialAlgorithm || stored.Iterations != credentialIterations {
		return Credentials{}, errors.New("unsupported credential file")
	}
	salt, err := decodeCredentialField(stored.Salt, credentialSaltBytes)
	if err != nil {
		return Credentials{}, fmt.Errorf("invalid credential salt: %w", err)
	}
	digest, err := decodeCredentialField(stored.Digest, credentialDigestBytes)
	if err != nil {
		return Credentials{}, fmt.Errorf("invalid credential digest: %w", err)
	}
	return Credentials{salt: salt, digest: digest}, nil
}

// InitializeCredentials creates a new credential store and returns its generated password.
func InitializeCredentials(path string) (string, error) {
	passwordBytes := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, passwordBytes); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(passwordBytes)
	credentials, err := credentialsForPassword(password)
	if err != nil {
		return "", err
	}
	data, err := marshalCredentials(credentials)
	if err != nil {
		return "", err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("create credential file: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return "", fmt.Errorf("write credential file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close credential file: %w", err)
	}
	committed = true
	return password, nil
}

// SetPassword replaces the credential store atomically with a new password.
func SetPassword(path, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	credentials, err := credentialsForPassword(password)
	if err != nil {
		return err
	}
	data, err := marshalCredentials(credentials)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("credential path must be a regular non-symlink file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect credential file: %w", err)
	}

	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".serverdesk-auth-*")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return fmt.Errorf("set credential file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write credential file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close credential file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	return nil
}

func credentialsForPassword(password string) (Credentials, error) {
	if err := validatePassword(password); err != nil {
		return Credentials{}, err
	}
	salt := make([]byte, credentialSaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return Credentials{}, fmt.Errorf("generate credential salt: %w", err)
	}
	digest, err := pbkdf2.Key(sha256.New, password, salt, credentialIterations, credentialDigestBytes)
	if err != nil {
		return Credentials{}, fmt.Errorf("derive credential digest: %w", err)
	}
	return Credentials{salt: salt, digest: digest}, nil
}

func validatePassword(password string) error {
	if len(password) < minimumPasswordBytes || len(password) > maximumPasswordBytes {
		return fmt.Errorf("password must be %d to %d bytes", minimumPasswordBytes, maximumPasswordBytes)
	}
	return nil
}

func marshalCredentials(credentials Credentials) ([]byte, error) {
	if len(credentials.salt) != credentialSaltBytes || len(credentials.digest) != credentialDigestBytes {
		return nil, errors.New("invalid credentials")
	}
	return json.Marshal(credentialFile{
		Version: credentialVersion, Username: Username, Algorithm: credentialAlgorithm,
		Iterations: credentialIterations,
		Salt:       base64.RawURLEncoding.EncodeToString(credentials.salt),
		Digest:     base64.RawURLEncoding.EncodeToString(credentials.digest),
	})
}

func decodeCredentialField(value string, length int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != length || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("must be canonical URL-safe base64")
	}
	return decoded, nil
}

func openCredentialFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect credential file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("credential path must be a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential file permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open credential file: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat credential file: %w", err)
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("credential file changed while opening")
	}
	return file, nil
}
