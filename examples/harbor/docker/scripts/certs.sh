#!/bin/sh
# 生成本地 CA，并签发 PostgreSQL 与管理服务的服务器证书（SAN：127.0.0.1、localhost）。
# 管理服务与构建 worker 要求以 verify-full 连接数据库，所以数据库也必须用可校验的证书。
# CA 私钥签完即删：这套 CA 之后不能再签发任何证书。已存在时不重复生成。
set -eu
dir=/certs
if [ -s "$dir/ca.crt" ] && [ -s "$dir/postgres.key" ] && [ -s "$dir/admin.key" ]; then
  echo "certificates already present"
  exit 0
fi
umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
openssl req -x509 -newkey rsa:3072 -sha256 -days 825 -nodes -subj "/CN=TBCG harbor local CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -keyout "$work/ca.key" -out "$dir/ca.crt" 2>/dev/null
for name in postgres admin; do
  openssl req -new -newkey rsa:2048 -nodes -subj "/CN=127.0.0.1" -keyout "$dir/$name.key" -out "$work/$name.csr" 2>/dev/null
  printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nbasicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' >"$work/$name.ext"
  openssl x509 -req -in "$work/$name.csr" -CA "$dir/ca.crt" -CAkey "$work/ca.key" -set_serial "0x$(openssl rand -hex 16)" \
    -days 825 -sha256 -extfile "$work/$name.ext" -out "$dir/$name.crt" 2>/dev/null
done
# 数据库与各服务都以镜像中的 postgres 用户（uid 999）运行。
chown 999:999 "$dir"/*.key
chmod 0600 "$dir"/*.key
chmod 0644 "$dir"/*.crt
chmod 0755 "$dir"
echo "issued local CA and server certificates"
