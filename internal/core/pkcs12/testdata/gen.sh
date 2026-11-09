#!/usr/bin/env bash
# gen.sh: regenerate the PKCS#12 test fixtures with OpenSSL 3.4 or later
# (the legacy provider is needed for the RC2/RC4 files). The keys and
# certificates are throwaway test material. Run from anywhere:
#
#   internal/core/pkcs12/testdata/gen.sh
#
# Every .p12 uses the password in PASS unless its name says otherwise.
# The profiles mimic what other tools write:
#   windows-aes256-sha256  Windows "AES256-SHA256" export: PBES2 (PBKDF2
#                          HMAC-SHA256, AES-256-CBC) for key and certs,
#                          HMAC-SHA256 MAC, 2000 iterations
#   windows-3des-sha1      Windows "TripleDES-SHA1" export: PKCS#12 PBE
#                          SHA1+3DES for key and certs, HMAC-SHA1 MAC
#   macos-keychain         macOS Keychain export: SHA1+3DES key, SHA1+RC2-40
#                          certs, HMAC-SHA1 MAC
#   openssl-default        OpenSSL 3 defaults (PBES2 AES-256, SHA-256 MAC)
#   openssl-legacy         openssl pkcs12 -legacy (3DES key, RC2 certs)
set -euo pipefail
cd "$(dirname "$0")"

PASS='correct horse'
DAYS=36500
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# A root CA, an intermediate CA and client certificates for RSA, ECDSA
# P-256 and Ed25519 keys.
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$tmp/root.key" -out root.crt \
	-days $DAYS -subj "/CN=PostSonde Test Root CA" \
	-addext basicConstraints=critical,CA:true -addext keyUsage=critical,keyCertSign
openssl req -newkey rsa:2048 -nodes -keyout "$tmp/inter.key" -out "$tmp/inter.csr" \
	-subj "/CN=PostSonde Test Intermediate CA"
openssl x509 -req -in "$tmp/inter.csr" -CA root.crt -CAkey "$tmp/root.key" -out inter.crt \
	-days $DAYS -set_serial 2 -extfile <(printf 'basicConstraints=critical,CA:true\nkeyUsage=critical,keyCertSign\n')
leaf() { # leaf <name> <openssl genpkey args...>
	local name=$1
	shift
	openssl genpkey "$@" -out "$name.key"
	openssl req -new -key "$name.key" -out "$tmp/$name.csr" -subj "/CN=PostSonde Test Client $name"
	openssl x509 -req -in "$tmp/$name.csr" -CA inter.crt -CAkey "$tmp/inter.key" -out "$name.crt" \
		-days $DAYS -set_serial "0x$(openssl rand -hex 8)" \
		-extfile <(printf 'extendedKeyUsage=clientAuth\n')
}
leaf rsa -algorithm RSA -pkeyopt rsa_keygen_bits:2048
leaf ec -algorithm EC -pkeyopt ec_paramgen_curve:P-256
leaf ed25519 -algorithm ED25519
cat inter.crt root.crt >"$tmp/chain.pem"

export_p12() { # export_p12 <out> <key> <cert> <extra openssl pkcs12 args...>
	local out=$1 key=$2 crt=$3
	shift 3
	openssl pkcs12 -export -inkey "$key" -in "$crt" -certfile "$tmp/chain.pem" \
		-name "PostSonde Test Client" -out "$out" "$@"
}

export_p12 windows-aes256-sha256.p12 rsa.key rsa.crt -passout "pass:$PASS" \
	-keypbe AES-256-CBC -certpbe AES-256-CBC -macalg sha256 -iter 2000
export_p12 windows-3des-sha1.p12 rsa.key rsa.crt -passout "pass:$PASS" \
	-keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1 -iter 2000
export_p12 macos-keychain.p12 rsa.key rsa.crt -passout "pass:$PASS" -legacy \
	-keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-RC2-40 -macalg sha1
export_p12 openssl-default.p12 rsa.key rsa.crt -passout "pass:$PASS"
export_p12 openssl-legacy.p12 rsa.key rsa.crt -passout "pass:$PASS" -legacy
export_p12 ec-aes128.p12 ec.key ec.crt -passout "pass:$PASS" \
	-keypbe AES-128-CBC -certpbe AES-192-CBC -macalg sha512
export_p12 ed25519-pbes2-3des.p12 ed25519.key ed25519.crt -passout "pass:$PASS" \
	-keypbe DES-EDE3-CBC -certpbe DES-EDE3-CBC -macalg sha384
export_p12 rsa-2des.p12 rsa.key rsa.crt -passout "pass:$PASS" -legacy \
	-keypbe PBE-SHA1-2DES -certpbe NONE -macalg sha1
export_p12 rsa-rc4.p12 rsa.key rsa.crt -passout "pass:$PASS" -legacy \
	-keypbe PBE-SHA1-RC4-128 -certpbe NONE
export_p12 rc2-128-rc4-40.p12 ec.key ec.crt -passout "pass:$PASS" -legacy \
	-keypbe PBE-SHA1-RC4-40 -certpbe PBE-SHA1-RC2-128 -macalg sha1
openssl pkcs12 -export -inkey rsa.key -in rsa.crt -name "PostSonde Test Client" -out pbes1-md5des.p12 \
	-passout "pass:$PASS" -legacy -keypbe PBE-MD5-DES -certpbe NONE
export_p12 pbmac1.p12 ec.key ec.crt -passout "pass:$PASS" -pbmac1_pbkdf2 -pbmac1_pbkdf2_md sha512
export_p12 unencrypted-nomac.p12 ec.key ec.crt -passout "pass:$PASS" -keypbe NONE -certpbe NONE -nomac
export_p12 nomac-3des.p12 rsa.key rsa.crt -passout "pass:$PASS" -nomac \
	-keypbe PBE-SHA1-3DES -certpbe NONE
export_p12 empty-password.p12 rsa.key rsa.crt -passout pass:
export_p12 unicode-password.p12 ec.key ec.crt -passout 'pass:pässwörd €'
# Two files without a usable key/certificate pair.
openssl pkcs12 -export -nokeys -in rsa.crt -certfile "$tmp/chain.pem" -out certs-only.p12 -passout "pass:$PASS"
openssl pkcs12 -export -nocerts -inkey ec.key -out key-only.p12 -passout "pass:$PASS"

# Encrypted PKCS#8 keys (ENCRYPTED PRIVATE KEY), for DecryptPKCS8.
openssl pkcs8 -topk8 -in rsa.key -out rsa-pbes2-sha1prf.p8 -outform DER \
	-v2 aes-128-cbc -v2prf hmacWithSHA1 -passout "pass:$PASS"
openssl pkcs8 -topk8 -in ec.key -out ec-pbes2-sha512prf.p8 -outform DER \
	-v2 aes-256-cbc -v2prf hmacWithSHA512 -iter 10000 -passout "pass:$PASS"
openssl pkcs8 -topk8 -in ec.key -out ec-pkcs12-3des.p8 -outform DER \
	-v1 PBE-SHA1-3DES -passout "pass:$PASS"
openssl pkcs8 -topk8 -in rsa.key -out rsa-pbes2-rc2.p8 -outform DER \
	-v2 rc2-cbc -passout "pass:$PASS" -provider legacy -provider default
openssl pkcs8 -topk8 -in ec.key -out ec-pbes2-rc2-40.p8 -outform DER \
	-v2 rc2-40-cbc -passout "pass:$PASS" -provider legacy -provider default
openssl pkcs8 -topk8 -in rsa.key -out rsa-pbes1-md5des.p8 -outform DER \
	-v1 PBE-MD5-DES -passout "pass:$PASS" -provider legacy -provider default

echo "gen.sh: ok"
