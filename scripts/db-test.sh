#!/bin/sh
# Tes database browser terhadap MySQL dan PostgreSQL sungguhan (internal/dbx, real_test.go).
# Skrip ini menyalakan container sekali pakai bernama nb-dbtest-<pid>-*, menjalankan tesnya, lalu
# menghentikan dan menghapus HANYA container itu. Container lain di mesin ini tidak disentuh.
set -eu

PREFIX="nb-dbtest-$$"
MYSQL_IMAGE="${NB_DBTEST_MYSQL_IMAGE:-mysql:8.4}"
PG_IMAGE="${NB_DBTEST_PG_IMAGE:-postgres:18.6-alpine}"
PW="nbtest-$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"

if ! command -v docker >/dev/null 2>&1; then
	echo "docker tidak ada: tes database dilewati"
	exit 0
fi

cleanup() {
	for c in "$PREFIX-mysql" "$PREFIX-pg"; do
		docker rm -f "$c" >/dev/null 2>&1 || true
	done
}
trap cleanup EXIT INT TERM

free_port() {
	python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

MY_PORT=$(free_port)
PG_PORT=$(free_port)

echo "menyalakan $PREFIX-mysql ($MYSQL_IMAGE) di 127.0.0.1:$MY_PORT"
docker run -d --name "$PREFIX-mysql" -p "127.0.0.1:$MY_PORT:3306" \
	-e MYSQL_ROOT_PASSWORD="$PW" -e MYSQL_DATABASE=nbtest "$MYSQL_IMAGE" >/dev/null
echo "menyalakan $PREFIX-pg ($PG_IMAGE) di 127.0.0.1:$PG_PORT"
docker run -d --name "$PREFIX-pg" -p "127.0.0.1:$PG_PORT:5432" \
	-e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=nbtest "$PG_IMAGE" >/dev/null

wait_for() {
	name=$1
	shift
	i=0
	until docker exec "$name" "$@" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ "$i" -gt 90 ]; then
			echo "$name tidak siap dalam 90 detik"
			docker logs "$name" 2>&1 | tail -20
			exit 1
		fi
		sleep 1
	done
}
wait_for "$PREFIX-pg" pg_isready -U postgres -d nbtest
# mysqld menyala dua kali saat inisialisasi: tunggu sampai login dengan password berhasil
wait_for "$PREFIX-mysql" mysql -h127.0.0.1 -uroot -p"$PW" -e "SELECT 1" nbtest
echo "server siap"

NEXTBOARD_DB_TEST_MYSQL="127.0.0.1:$MY_PORT root $PW nbtest" \
NEXTBOARD_DB_TEST_POSTGRES="127.0.0.1:$PG_PORT postgres $PW nbtest" \
	go test -count=1 -run 'Real' ./internal/dbx "$@"
