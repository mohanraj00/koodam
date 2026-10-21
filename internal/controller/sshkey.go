/*
Copyright 2026 Mohan Raj.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
)

// The SSH key code uses only the standard library. golang.org/x/crypto/ssh is BSD-3-Clause, and
// the project permits only Apache-2.0 and MIT dependencies.

const sshEd25519 = "ssh-ed25519"

// sshString appends b with a uint32 length in front, as RFC 4251 section 5 tells.
func sshString(out, b []byte) []byte {
	out = binary.BigEndian.AppendUint32(out, uint32(len(b)))
	return append(out, b...)
}

// newHostKey makes an ed25519 SSH host key. It returns the private key in the OpenSSH format
// (PEM, no passphrase) and the public key in the authorized_keys format. The caller writes the
// private key only to a Secret.
func newHostKey() (private []byte, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	blob := sshString(sshString(nil, []byte(sshEd25519)), pub)

	// The 2 check numbers must be equal. sshd compares them to find a wrong passphrase.
	check := make([]byte, 4)
	if _, err := rand.Read(check); err != nil {
		return nil, "", err
	}
	sec := append(append([]byte{}, check...), check...)
	sec = sshString(sec, []byte(sshEd25519))
	sec = sshString(sec, pub)
	sec = sshString(sec, priv)
	sec = sshString(sec, nil) // no comment
	for i := byte(1); len(sec)%8 != 0; i++ {
		sec = append(sec, i)
	}

	// The cipher "none", the KDF "none", no KDF options and 1 key.
	body := sshString([]byte("openssh-key-v1\x00"), []byte("none"))
	body = sshString(body, []byte("none"))
	body = sshString(body, nil)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = sshString(body, blob)
	body = sshString(body, sec)

	private = pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: body})
	return private, sshEd25519 + " " + base64.StdEncoding.EncodeToString(blob), nil
}
