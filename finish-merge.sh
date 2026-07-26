#!/usr/bin/env bash
# Дожимает merge upstream/main в ветку feature/nft-usernames-and-rating.
# Запускать из корня репозитория:  bash finish-merge.sh
set -euo pipefail
cd "$(dirname "$0")"

echo "== 1/6 закрываю подвисший git am =="
# --quit, а не --abort: все патчи уже закоммичены, --abort откатил бы их.
if [ -d .git/rebase-apply ]; then git am --quit; echo "   состояние am убрано, HEAD не двигался"; else echo "   нечего закрывать"; fi

if [ ! -f .git/MERGE_HEAD ]; then
  echo "== merge не запущен, запускаю =="
  git merge --no-edit upstream/main || true
fi

echo "== 2/6 CSS: беру заранее разрешённую версию =="
cp merge-resolved-02-pages-and-forms.css cmd/telesrv-admin/web/src/styles/02-pages-and-forms.css

echo "== 3/6 ожидаемая версия схемы в тесте -> 156 =="
sed -i 's/status\.Version != 1[0-9][0-9]/status.Version != 156/; s/want clean version 1[0-9][0-9]/want clean version 156/' \
  internal/store/postgres/star_gift_lifecycle_migration_integration_test.go

echo "== 4/6 бандл админки: своя версия + пересборка =="
git checkout --ours -- cmd/telesrv-admin/web/dist 2>/dev/null || true
( cd cmd/telesrv-admin/web && rm -f dist/assets/index-*.js dist/assets/index-*.css && npm run build )

echo "== 5/6 фиксирую merge =="
git add -A
git commit --no-edit

echo "== 6/6 проверка =="
grep -rn "^<<<<<<< \|^>>>>>>> " --include=*.go --include=*.css --include=*.tsx --include=*.sql . && { echo "ОСТАЛИСЬ МАРКЕРЫ КОНФЛИКТА"; exit 1; } || echo "   маркеров конфликта нет"
go build ./...
echo "   сборка ок, гоняю тесты"
go test ./...
echo
echo "ГОТОВО. Дальше пересобери бинари:"
echo "  go build -o bin/gramsrv.exe ./cmd/telesrv && go build -o bin/telesrv-admin.exe ./cmd/telesrv-admin"
