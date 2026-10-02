# Changelog

Each release has a section in English and in Russian; the release workflow puts them in
the signed manifest, and the panel shows the one in its language.

## 0.4.5
### en
- Prepares the move to 0.5: the panel and the mikan command understand versions with four numbers (0.5.0.0), and the command updates itself before the panel when a release needs a newer one. Nothing else changes; the database stays as it is.
- After this update, 0.5.0.0 arrives the usual way: the Update button in Settings → Updates, or the nightly automatic update. It moves the panel to PostgreSQL with a backup first.

### ru
- Подготовка к переходу на 0.5: панель и команда mikan понимают версии из четырёх чисел (0.5.0.0), а команда сама обновляется раньше панели, если релизу нужна новая. Больше ничего не меняется, база остаётся прежней.
- После этого обновления 0.5.0.0 придёт обычным путём: кнопкой «Обновить» в «Настройки → Обновления» или ночным автообновлением. Оно переведёт панель на PostgreSQL, сначала сделав бэкап.

## 0.4.4
### en
- YooKassa and CryptoBot now come from the marketplace like every other payment method. On the update the panel moves their keys into the adapters, asks the server to install the adapter that took payments (Payments shows a notice until it runs), and keeps everything working: invoices opened before the update are paid through the adapter, the notification URLs set in the YooKassa and CryptoBot dashboards stay valid, and pay buttons in old bot messages still work. Payments → Accepting payments keeps Telegram Stars and the selling switches.
- Server security: the mikan command no longer trusts any file the panel can write and never follows links in the panel's folders, so a compromised panel container cannot reach root on the host. The panel and the node each see only their own data folder, /opt/mikan/data belongs to root, and the panel's memory and both containers' processes are capped. On the first run after the update the server rewrites compose.yaml (the old one is kept as compose.yaml.old) and restarts the containers once.
- install.sh checks the signature of the release and the installer's hash before it installs anything; releases are built only from main, and every CI action is pinned to a commit.
- Updates and backups: one lock for every server operation, an update that fails rolls back the settings and, when the old version cannot start, the database too; backups are private (0700/0600), restore checks the archive and takes a snapshot first, automatic backups are rotated.
- API keys: creating one or turning on 2FA asks for the password, changing the password revokes the admin's keys (a checkbox), a full key can no longer change addresses, keys, payments or the bot, a read key does not see subscription links. Logins are counted atomically and per IPv6 /64.
- A subscription linked in the bot moves to another Telegram account only when its owner agrees; the Mini App's sign-in lasts an hour. HSTS is sent while the panel's certificate is trusted. Names that lead to internal addresses are refused as REALITY targets and as the bot's proxy.
- Node sync: a node that is down is retried with a growing pause instead of every few seconds, one bad inbound no longer stops the whole state, counters a node cannot have carried are ignored, a node starts even with broken state files, and the panel stops its workers before closing the database.
- Bot: notices and Stars payments survive a restart or a Telegram outage, the update position is kept, calls have timeouts, broadcasts are queued in batches.
- Paid payments that could not be applied are retried until they apply instead of being dropped after a week; the payments history loads in one query.
- Faster: lighter admin and subscription pages (the Mini App loads less than half the code), indexes for traffic, devices and the audit log, the subscription config is kept for a few seconds, policies go to nodes only when they change. Old traffic and audit rows are cleaned up; idle devices are forgotten after 90 days, users without a device limit get at most 50.
- Interface: forms keep what you typed when data refreshes, a failed refresh keeps the data on screen with a notice, error and 404 pages, bulk actions touch only the visible rows, contrast up to AA. New GET /api/v1/audit.

### ru
- ЮKassa и CryptoBot теперь ставятся из маркетплейса, как и остальные способы оплаты. При обновлении панель сама переносит их ключи в адаптеры, просит сервер установить адаптер того, что принимало оплату (на «Платежах» висит уведомление, пока он не запустится), и ничего не ломает: счета, открытые до обновления, оплачиваются через адаптер, адреса уведомлений в кабинетах ЮKassa и CryptoBot остаются прежними, кнопки оплаты в старых сообщениях бота работают. В «Приёме оплаты» остаются Telegram Stars и переключатели продаж.
- Безопасность сервера: команда mikan больше не доверяет файлам, которые может записать панель, и не ходит по ссылкам в её каталогах, так что взломанный контейнер панели не доберётся до root на хосте. Панель и нода видят только свои каталоги данных, /opt/mikan/data принадлежит root, у панели ограничена память, у обоих контейнеров число процессов. При первом запуске после обновления сервер перепишет compose.yaml (старый останется как compose.yaml.old) и один раз перезапустит контейнеры.
- install.sh проверяет подпись релиза и хеш установщика до установки; релизы собираются только из main, все действия CI закреплены по коммиту.
- Обновления и бэкапы: одна блокировка на все операции сервера, неудачное обновление откатывает настройки, а если старая версия не стартует, то и базу; бэкапы закрыты (0700/0600), восстановление проверяет архив и сначала делает снимок, автоматические бэкапы ротируются.
- Ключи API: создание ключа и включение 2FA спрашивают пароль, смена пароля отзывает ключи админа (есть галочка), полный ключ больше не меняет адреса, ключи, платежи и бота, ключ на чтение не видит ссылки подписок. Попытки входа считаются атомарно и по IPv6 /64.
- Подписка, привязанная в боте, переходит на другой аккаунт Telegram только с согласия владельца; вход в Mini App действует час. HSTS отдаётся, пока сертификат панели доверенный. Имена, ведущие на внутренние адреса, не принимаются как цели REALITY и прокси бота.
- Синхронизация нод: недоступную ноду панель повторяет с растущей паузой, а не каждые несколько секунд, один сломанный inbound больше не стопорит всё состояние, невозможные счётчики трафика отбрасываются, нода стартует даже с битыми файлами состояния, панель останавливает фоновые задачи до закрытия базы.
- Бот: уведомления и оплаты Stars переживают перезапуск и сбой Telegram, позиция в потоке обновлений сохраняется, у вызовов есть таймауты, рассылка идёт пачками.
- Оплаченные, но не применённые платежи повторяются, пока не применятся, а не бросаются через неделю; история платежей грузится одним запросом.
- Быстрее: админка и страница подписки легче (Mini App грузит меньше половины прежнего кода), индексы для трафика, устройств и журнала, конфиг подписки держится несколько секунд, политики уходят на ноды только при изменениях. Старые записи трафика и журнала чистятся; устройства без активности забываются через 90 дней, пользователю без лимита устройств доступно не больше 50.
- Интерфейс: формы не теряют введённое при обновлении данных, при неудачном обновлении данные остаются с пометкой, есть страницы ошибки и 404, массовые действия касаются только видимых строк, контраст до AA. Новый GET /api/v1/audit.

## 0.4.3
### en
- Protocols behind a TCP proxy (#11): a protocol's settings have "Behind a proxy (nginx, HAProxy)". "Where the node listens" picks all addresses (as before), localhost only or an IP of your own, so nginx stream or HAProxy can hold port 443 and route by SNI to protocols on 127.0.0.1:444, :445… "Address for clients", "Port for clients" and "SNI for clients" give subscriptions the proxy's endpoint instead of the node's (empty keeps them as now; with REALITY the camouflage site's domain stays the SNI). Such a protocol's port never moves on its own: automatic port moves are off and cannot be turned on, and the panel warns that an automatic camouflage site change may break SNI routing.
- The installer replaces a Docker without compose v2 (the docker.io of Ubuntu 22.04 and Debian) with Docker from get.docker.com, after asking: the interactive installer has a page for it, a plain install needs --replace-docker. Images, volumes and containers stay.
- A panel without a domain gets its Let's Encrypt IP certificate again: the request no longer puts the IP into the Common Name, which Let's Encrypt refused (badCSR).
- Payment settings that cannot be read (a busy database) are no longer replaced by the defaults and saved over yours: selling pauses until they read again. Changes on the Telegram page are saved whole or not at all.
- Traffic packages (#12): Plans → Traffic packages sells extra traffic for the main limit or a traffic pool (Stars, YooKassa, CryptoBot), valid until used up, until the period ends or for N days. The bot has "Buy more traffic" in the subscription screen, the Mini App shows packages for the subscription on screen, and a user's card lists them with "Add traffic" for a bonus or a compensation. The plan's traffic is spent first, then the packages, the one that expires sooner first; what is left carries over the traffic reset. A user or a pool that ran out works again right after a purchase, without a new link.
- Payment methods from the marketplace: Payments → "Payment methods from the marketplace" → Add lists the signed catalog of getmikan/marketplace (YooKassa and CryptoBot to start with) and installs an adapter on the server as a container of its own; its settings form, the notification URL for the provider's dashboard and the switch are on the same card. The bot and the Mini App get a pay button for it, for plans and traffic packages. The panel never trusts a provider's notification alone: it asks the adapter for the invoice and compares the amount and the currency with the payment it made. On the server: mikan addon list, install, remove. The built-in YooKassa and CryptoBot keep working as before.
- Easier to find things: Settings are split into General, Subscription, Clash rules and Security; the Telegram page into Connection, Menu and texts, Notifications and Broadcast (unsaved changes follow you between them); Plans into Plans, Traffic pools and Traffic packages. A plan's card shows its pool limits, a pool shows its protocols and its limit in each plan. Selling can be switched on right from the Payments page.
- A domain is taken only when it leads to this server: the panel's domain in Settings and a node's domain are checked with public DNS when they or the server's address change, and saving says where the domain points instead. The installer now also stops on a domain with an extra A record or an AAAA record elsewhere.
- Safer edits: a protocol's change that is refused (a busy port, a bad config) no longer leaves part of it saved. "Port is taken" is one rule everywhere (the panel, mikan inbound, the automatic moves): it now sees Hysteria2 port ranges, a cascade's relay, the panel's port and a node's command port. A node that a cascade or the bot's route to Telegram goes through is no longer deleted silently: the panel lists what uses it. Every error the panel can show has a text, and server addresses are checked by one rule in the panel and the installer.

### ru
- Подключения за TCP-прокси (#11): в настройках подключения — блок «За прокси (nginx, HAProxy)». «Где нода слушает» — все адреса (как раньше), только localhost или свой IP: так nginx stream или HAProxy держит порт 443 и раздаёт по SNI подключениям на 127.0.0.1:444, :445… «Адрес», «Порт» и «SNI для клиентов» отдают в подписке адрес прокси вместо адреса ноды (пусто — как сейчас; у REALITY SNI остаётся доменом сайта маскировки). Порт такого подключения сам не меняется: автоперенос порта выключен и не включается, а про автосмену сайта маскировки панель предупреждает — она может сбить маршрут по SNI.
- Установщик заменяет Docker без compose v2 (docker.io из Ubuntu 22.04 и Debian) на Docker с get.docker.com, но сначала спрашивает: в интерактивном установщике для этого отдельный экран, без него нужен флаг --replace-docker. Образы, тома и контейнеры остаются.
- Панель без домена снова получает сертификат Let's Encrypt на IP: в запросе больше нет IP в поле Common Name, из-за которого Let's Encrypt отказывал (badCSR).
- Настройки оплаты, которые не удалось прочитать (занятая база), больше не подменяются значениями по умолчанию и не записываются поверх ваших: продажи ставятся на паузу, пока чтение не наладится. Изменения на странице Telegram сохраняются целиком или не сохраняются вовсе.
- Пакеты трафика (#12): «Тарифы → Пакеты трафика» продают дополнительный трафик для основного лимита или пула (Stars, ЮKassa, CryptoBot) — пока не израсходован, до конца периода или на N дней. В боте в экране подписки есть «Докупить трафик», в Mini App — пакеты для открытой подписки, а в карточке пользователя — список пакетов и «Начислить трафик» для бонуса или компенсации. Сначала тратится трафик тарифа, потом пакеты, первым — тот, что раньше истекает; остаток переходит через сброс трафика. Пользователь или пул, у которых кончился трафик, снова работают сразу после покупки, без новой ссылки.
- Способы оплаты из маркетплейса: «Платежи → Способы оплаты из маркетплейса → Добавить» показывает подписанный каталог getmikan/marketplace (для начала ЮKassa и CryptoBot) и ставит адаптер на сервер отдельным контейнером; там же форма его настроек, адрес для уведомлений в кабинете платёжной системы и переключатель. В боте и Mini App у него своя кнопка оплаты — для тарифов и пакетов трафика. Уведомлению платёжной системы панель сама по себе не верит: она спрашивает адаптер о счёте и сверяет сумму и валюту со своим платежом. На сервере: mikan addon list, install, remove. Встроенные ЮKassa и CryptoBot работают как раньше.
- Проще найти нужное: «Настройки» разделены на «Основное», «Подписку», «Правила Clash» и «Безопасность»; «Telegram» — на «Подключение», «Меню и тексты», «Уведомления» и «Рассылку» (несохранённые правки не теряются при переходе); «Тарифы» — на тарифы, пулы и пакеты трафика. В карточке тарифа видны лимиты пулов, у пула — его подключения и лимит в каждом тарифе. Продажу можно включить прямо на странице «Платежи».
- Домен принимается, только если он ведёт на этот сервер: домен панели в «Настройках» и домен ноды проверяются через публичный DNS при смене домена или адреса сервера, а при ошибке видно, куда домен указывает на самом деле. Установщик тоже останавливается, если у домена есть лишняя A-запись или AAAA-запись на другой сервер.
- Надёжнее правки: отклонённое изменение подключения (занятый порт, ошибка в конфиге) больше не оставляет половину сохранённой. «Порт занят» — одно правило везде (панель, mikan inbound, автоисправления): теперь оно видит диапазоны портов Hysteria2, relay каскада, порт панели и порт команд ноды. Ноду, через которую идёт каскад или путь бота в Telegram, больше нельзя удалить молча — панель показывает, что от неё зависит. У каждой ошибки панели есть понятный текст, а адреса серверов проверяются одним правилом в панели и установщике.

## 0.4.2
### en
- ARM servers (Raspberry Pi 4 and other arm64 machines) install again: the arm64 image carried x86-64 binaries and stopped with "exec format error". The image build now fails if a binary does not match its architecture. A Raspberry Pi needs a 64-bit OS.
- API moved from the sidebar to Settings → API (old /api-docs links lead there). The page has "Download OpenAPI": openapi.json with this panel's address already in it, ready for Postman, Insomnia, Swagger UI and client generators.
- Payments warns when the bot has nothing to sell: no plan is on sale with a price for a method that takes payments, so people would only be told to message support. A button leads to Plans.
- Selling subscriptions has its own switch in Settings. Off, the bot and the Mini App sell nothing, Payments leaves the menu and plans hide their prices; invoices opened before are still applied. New panels start with it off; panels that already set up payments keep selling.
- The bot can reach Telegram through a node or a proxy, for a server where Telegram is blocked: Telegram → Way to Telegram picks Direct, Via a node (a remote node of the panel; it passes only requests to Telegram, nodes need 0.4.2) or Via a proxy (SOCKS5 or HTTP(S), the password is not shown again). The route is checked before it is saved. Connecting a bot from such a server now says Telegram is unreachable instead of "server error".
- Subscription port: Settings → Subscription port moves subscription links and the subscription page to a port of their own, such as 443 or 2053, at once and without a restart. The admin panel does not open there and its port stops showing in links; links on the panel's port keep working, so clients change nothing. The ports opened at install are one click away, those taken by a protocol are marked, and a protocol of the panel's own node can no longer take the subscription port (by hand or by the automatic port moves).
- Hysteria2 with Gecko obfuscation: the new "Hysteria2 · Gecko" protocol, or Obfuscation → Gecko in a Hysteria2 protocol's settings. Gecko cuts the QUIC handshake into padded pieces of random size, against DPI that tells QUIC by packet sizes. Only mihomo apps that name a core of 1.19.26 or later get it (an older core would fail the whole profile on it); the rest go on with plain Hysteria2, so keep one next to it. Port hopping works with it as with any Hysteria2: a port range such as 20000-30000.
- Clash rules of your own: Settings → Clash rules takes one rule per line (DOMAIN-SUFFIX, GEOSITE, IP-CIDR, PROCESS-NAME and more, to DIRECT, REJECT, PROXY or a subscription group). They go before the routing mode in every Clash profile; the panel's and the nodes' addresses still go direct. Each line is checked on save and a mistake names its line, so a typo never breaks clients' profiles; the rule types an older mihomo core lacks reach only apps that name a core with them. Example rules are one click away.
- Own TLS certificates (#9): Settings → Panel certificate → Own certificate takes a chain and its key (files or pasted) instead of Let's Encrypt, so port 80 is no longer needed: a DNS wildcard from certbot, a Cloudflare Origin certificate or your own CA's. Nodes → Certificate does the same for a node's Hysteria2, TUIC, AnyTLS and TrustTunnel; a publicly trusted one goes to clients without a pin, so renewing it breaks nothing. On the server, `mikan cert set --cert fullchain.pem --key privkey.pem [--node N]` fits a certbot or acme.sh renewal hook, and a renewed file is served without a restart (at once, or within half a minute). The key never comes back from the panel; an expired certificate falls back to the automatic one and says so.

### ru
- Установка на ARM-серверах (Raspberry Pi 4 и другие arm64) снова работает: в arm64-образе лежали бинарники для x86-64, и установка падала с «exec format error». Теперь сборка образа падает, если бинарник не под его архитектуру. Raspberry Pi нужна 64-битная ОС.
- Раздел API переехал из бокового меню в «Настройки → API» (старые ссылки /api-docs ведут туда). На странице есть «Скачать OpenAPI»: openapi.json с уже прописанным адресом этой панели — для Postman, Insomnia, Swagger UI и генераторов клиентов.
- «Платежи» предупреждают, когда боту нечего продавать: нет тарифа «В продаже» с ценой для способа, который принимает оплату, — люди увидят только «напишите в поддержку». Кнопка ведёт в «Тарифы».
- У продажи подписок свой переключатель в «Настройках». Выключено — бот и Mini App ничего не продают, «Платежи» пропадают из меню, а тарифы прячут цены; счета, открытые раньше, всё равно засчитываются. На новых панелях продажи выключены; панели, где оплату уже настроили, продолжают продавать.
- Бот может ходить в Telegram через ноду или прокси — для сервера, где Telegram заблокирован: «Telegram → Связь с Telegram» — «Напрямую», «Через ноду» (удалённая нода панели; пропускает только запросы к Telegram, нужна нода 0.4.2) или «Через прокси» (SOCKS5 или HTTP(S), пароль больше не показывается). Путь проверяется перед сохранением. Подключение бота с такого сервера теперь пишет, что нет связи с Telegram, а не «Ошибка сервера».
- Порт подписки: «Настройки → Порт подписки» переносит ссылки подписок и страницу подписки на отдельный порт, например 443 или 2053, — сразу и без перезапуска. Админка на нём не открывается, а её порт больше не виден в ссылках; старые ссылки на порту панели продолжают работать, клиентам ничего менять не нужно. Порты, открытые при установке, выбираются в один клик, занятые подключениями помечены, а подключения своей ноды больше не могут занять порт подписки — ни вручную, ни автоподбором.
- Hysteria2 с обфускацией Gecko: новый протокол «Hysteria2 · Gecko» или «Обфускация → Gecko» в настройках Hysteria2. Gecko режет рукопожатие QUIC на куски случайного размера с добивкой — против DPI, который узнаёт QUIC по размерам пакетов. Его получают только приложения на ядре mihomo 1.19.26+, которые сообщают версию ядра (старое ядро не загрузило бы весь профиль); остальным приходит обычный Hysteria2, поэтому держите его рядом. Порт-хоппинг работает как у любого Hysteria2: диапазон портов вроде 20000-30000.
- Свои правила Clash: «Настройки → Правила Clash» — по правилу в строке (DOMAIN-SUFFIX, GEOSITE, IP-CIDR, PROCESS-NAME и другие; куда — DIRECT, REJECT, PROXY или группа подписки). Они встают перед режимом маршрутизации в каждом Clash-профиле; адреса панели и нод по-прежнему идут напрямую. Каждая строка проверяется при сохранении, ошибка называет номер строки — опечатка не сломает профиль у клиентов; типы правил, которых нет в старом ядре mihomo, получают только приложения, сообщающие версию ядра. Готовые примеры добавляются в один клик.
- Свои TLS-сертификаты (#9): «Настройки → Сертификат панели → Свой сертификат» принимает цепочку и ключ (файлами или текстом) вместо Let's Encrypt — порт 80 больше не нужен: wildcard от certbot по DNS, Cloudflare Origin или сертификат вашего центра. «Ноды → Сертификат» делает то же для Hysteria2, TUIC, AnyTLS и TrustTunnel ноды; публично доверенный уходит клиентам без пина, и его продление ничего не ломает. На сервере `mikan cert set --cert fullchain.pem --key privkey.pem [--node N]` подходит для хука продления certbot или acme.sh, а обновлённый файл подхватывается без перезапуска (сразу или в течение полуминуты). Ключ из панели никогда не отдаётся; истёкший сертификат сменяется автоматическим, и панель об этом пишет.

## 0.4.1
### en
- Traffic pools (#6): chosen protocols can count to a pool with its own limit, apart from the main traffic — a WL node at 100 GB a month while Germany and Estonia stay unlimited, in one subscription. Make pools on the Plans page, put a protocol into one in its settings, give the pool a limit in the plan or per user. When a pool runs out only its protocols stop (and leave the subscription until the reset); everything else keeps working, and the other way round. Pools reset with the main traffic; the user card, the subscription page and the bot show each pool.
- Server cascades: a protocol can send its traffic out through another node of the panel — client → node A → node B → internet, so sites see B's address while clients connect to A. Pick "Way out: Via a node" in a protocol's settings; the panel opens a hidden relay on the exit node by itself, with a key per source node. Chains of three or more servers work too (Nodes → Cascade sets where a node sends other nodes' traffic next: direct, its WARP or one more node), loops are refused, and WARP is not needed. Traffic is counted once, on the first node; when the exit node is down the traffic does not fall back to the first node's address. Nodes → Cascade shows the address sites see through each chain.
- TLS fingerprints: besides the list, "Own…" takes any uTLS profile name (lowercase letters, digits, _), such as chrome120 or randomizednoalpn, in Settings and in a protocol's settings.
- The installer no longer stops at "Package manager: busy" on Debian 12: without the psmisc package it took the always-running unattended-upgrades helper for an apt run and gave up after 10 minutes. It now waits only while apt or dpkg really holds its locks, and names the process it waits for.

### ru
- Пулы трафика (#6): часть подключений может считаться в пул со своим лимитом, отдельно от основного трафика — WL-нода на 100 ГБ в месяц, а Германия и Эстония без лимита, в одной подписке. Пулы создаются на странице «Тарифы», подключение добавляется в пул в своих настройках, лимит пула задаётся в тарифе или у пользователя. Когда пул кончился, перестают работать только его подключения (и до сброса пропадают из подписки), остальное работает — и наоборот. Пулы сбрасываются вместе с основным трафиком; карточка пользователя, страница подписки и бот показывают каждый пул.
- Каскад серверов: подключение может выпускать трафик через другую ноду панели — клиент → нода A → нода B → интернет, сайты видят адрес B, а клиенты подключаются к A. В настройках подключения выберите «Выход в интернет: Через ноду»; служебный вход на ноде выхода панель откроет сама, с отдельным ключом для каждой ноды. Работают и цепочки из трёх и более серверов («Ноды → Каскад»: куда нода выпускает трафик других нод — напрямую, через свой WARP или дальше через ещё одну ноду), петли панель не даст сохранить, WARP не обязателен. Трафик считается один раз, на первой ноде; если нода выхода недоступна, трафик не уходит с адреса первой ноды. «Ноды → Каскад» показывает, какой адрес видят сайты через каждую цепочку.
- TLS-отпечатки: кроме списка есть «Своё…» — любое имя профиля uTLS (латиница в нижнем регистре, цифры, _), например chrome120 или randomizednoalpn, в «Настройках» и в настройках подключения.
- Установщик больше не останавливается на «Package manager: busy» на Debian 12: без пакета psmisc он принимал постоянно запущенный фоновый процесс unattended-upgrades за работу apt и сдавался через 10 минут. Теперь он ждёт, только пока apt или dpkg действительно держат свои блокировки, и пишет, какой процесс ждёт.

## 0.4.0
### en
- Pick the TLS fingerprint clients send: Settings → Subscription sets the default for all protocols, and a protocol's settings can choose its own (Chrome, Firefox, Safari, iOS, Android, Edge, 360, QQ or a random one). Links (`fp=`) and Clash profiles (`client-fingerprint`) follow the choice; Hysteria2 and TUIC have no such fingerprint.
- API keys and an API reference: the new API page makes keys for scripts and integrations (`Authorization: Bearer`, read-only or full access, an optional expiry, revocable) and lists every method with its parameters, responses, curl examples and a "Try it" button for GET requests.
- Selling subscriptions: mark a plan "On sale" with a price in Telegram Stars and/or rubles, turn payment methods on in the new Payments section (Telegram Stars needs only the bot; YooKassa for cards and SBP; CryptoBot for crypto), and people buy or renew in the bot and the Mini App. The panel creates or renews the subscription as soon as the provider confirms the payment and the bot sends the link. A renewal adds the plan's term after the current one and, unless switched off in Payments, starts a new traffic period. Payments has the history and Stars refunds.
- Cloudflare WARP as a way out for chosen protocols (#4): Nodes → WARP registers a free account in one click (a WARP+ key is optional) or takes your own WireGuard config. A protocol's settings pick "Way out: WARP", and a list of domains and networks goes through WARP for every protocol of the node; the rest goes direct. When WARP is down its traffic does not fall back to the server's own address. The node shows the address sites see through WARP.

### ru
- Выбор TLS-отпечатка клиентов: в «Настройки → Подписка» — общий для всех протоколов, в настройках протокола — свой (Chrome, Firefox, Safari, iOS, Android, Edge, 360, QQ или случайный). Ссылки (`fp=`) и Clash-профили (`client-fingerprint`) берут выбранный; у Hysteria2 и TUIC такого отпечатка нет.
- Ключи API и справочник: в новом разделе «API» создаются ключи для скриптов и интеграций (`Authorization: Bearer`, только чтение или полный доступ, срок по желанию, отзыв), а все методы описаны с параметрами, ответами, примерами curl и кнопкой «Выполнить» для GET-запросов.
- Продажа подписок: отметьте тариф «В продаже» с ценой в Telegram Stars и/или рублях, включите способы оплаты в новом разделе «Платежи» (Telegram Stars — нужен только бот; ЮKassa — карты и СБП; CryptoBot — криптовалюта), и люди покупают и продлевают подписку в боте и Mini App. Панель создаёт или продлевает подписку, как только провайдер подтвердил оплату, а бот присылает ссылку. Продление добавляет срок тарифа после текущего и, если не выключено в «Платежах», начинает новый период трафика. В «Платежах» — история и возврат Stars.
- Cloudflare WARP как выход для выбранных подключений (#4): «Ноды → WARP» регистрирует бесплатный аккаунт в один клик (ключ WARP+ — по желанию) или принимает свой WireGuard-конфиг. В настройках подключения выбирается «Выход в интернет: WARP», а список доменов и сетей идёт через WARP у всех подключений ноды; остальное — напрямую. Если WARP недоступен, его трафик не уходит с адреса сервера. Нода показывает адрес, который видят сайты через WARP.

## 0.3.9
### en
- The panel has a default language, picked at install: the admin panel and the subscription page open in it until a visitor picks their own, and default names (tariffs, the auto-select group, the bot's menu) are in it. Settings → Default language changes it; "Browser language" keeps the old behaviour.
- The server's command line (`mikan admin …`) is in English.
- A node joins with one command that installs everything from the latest release; the Nodes page shows it with the join key.
- A new installer and server menu in the terminal. First the panel's language, then checks of the server and of the domain's DNS, REALITY sites next to the server, and the admin's login with a QR code. Later runs of `mikan` open a menu: status, updates, logs, access, REALITY sites, nodes, backups.
- Releases come from GitHub: the image from GitHub Packages, trusted through a signed manifest. Updates back up first and go back when the new version does not start.
- The panel looks for a new release once a day and shows what changed. Settings → Updates installs it with a button or turns on automatic updates at night; a badge in the sidebar tells when one is out.

### ru
- У панели есть язык по умолчанию, его выбирают при установке: админка и страница подписки открываются на нём, пока человек не выбрал свой, и на нём же названия по умолчанию (тарифы, группа автовыбора, меню бота). Меняется в «Настройки → Язык по умолчанию»; «Как в браузере» — прежнее поведение.
- Серверные команды (`mikan admin …`) — на английском.
- Нода подключается одной командой, которая ставит всё из последнего релиза; страница «Ноды» показывает её вместе с ключом.
- Новый установщик и меню сервера в терминале. Сначала язык панели, потом проверки сервера и DNS домена, сайты REALITY рядом с сервером и вход администратора с QR-кодом. Повторный запуск `mikan` открывает меню: состояние, обновления, логи, доступ, сайты REALITY, ноды, бэкапы.
- Релизы приходят с GitHub: образ из GitHub Packages, доверие через подписанный манифест. Обновление сначала делает бэкап и откатывается, если новая версия не запустилась.
- Панель раз в сутки проверяет новые релизы и показывает, что изменилось. «Настройки → Обновления» ставят релиз по кнопке или включают автообновление ночью; значок в боковой панели подскажет, когда вышла новая версия.

## 0.3.8
### en
- The Telegram tab of the admin panel animates like the others: cards rise in turn, the unsaved-changes bar slides in and out, menu buttons slide to their new place.

### ru
- Вкладка Telegram в админке анимирована как остальные: карточки выезжают по очереди, плашка несохранённых изменений выезжает и уезжает, кнопки меню плавно переставляются.

## 0.3.7
### en
- The Telegram bot sends through a queue within Telegram's limits: replies first, then notices, then broadcasts; fast taps show the last screen; flood waits are waited out.
- Notices at night (22:00–9:00 Moscow time) arrive silently; broadcasts show their progress.
- New "dawn" background in the panel.
- The Mini App button next to the chat's input field is one word, so the field is not pushed out on phones.
- A REALITY target given by IP shows and checks its site name (SNI).

### ru
- Telegram-бот отправляет сообщения через очередь в рамках лимитов Telegram: сначала ответы, потом уведомления, потом рассылки; при быстрых нажатиях виден последний экран; флуд-ожидания выдерживаются.
- Уведомления ночью (22:00–9:00 МСК) приходят без звука; у рассылки виден прогресс.
- Новый фон панели «Рассвет».
- Кнопка Mini App у поля ввода — одно слово, поле больше не пропадает на телефонах.
- У цели REALITY по IP видно и проверяется имя сайта (SNI).

## 0.3.6
### en
- Telegram bot for subscribers with a menu builder in the panel, notifications and broadcasts.
- The subscription page opens as a Telegram Mini App.

### ru
- Telegram-бот для подписчиков с конструктором меню в панели, уведомлениями и рассылками.
- Страница подписки открывается как Mini App в Telegram.

## 0.3.5
### en
- New protocols: TrustTunnel, ShadowQUIC, Mieru; shared-key Shadowsocks-2022, Sudoku, Snell.
- Subscriptions give every app only the protocols it can run.

### ru
- Новые протоколы: TrustTunnel, ShadowQUIC, Mieru; с общим ключом — Shadowsocks-2022, Sudoku, Snell.
- Подписка отдаёт каждому приложению только те протоколы, которые оно умеет.

## 0.3.4
### en
- Automatic moves judge a port by the devices that reached it before; no more false "devices cannot connect".

### ru
- Автоподбор судит о порте по устройствам, которые до него раньше доходили; ложное «не доходят устройства» ушло.

## 0.3.3
### en
- Billing days and device binding against key sharing.
- VLESS with post-quantum encryption (VLESS PQ).

### ru
- День оплаты и привязка к устройствам против перепродажи ключа.
- VLESS с постквантовым шифрованием (VLESS PQ).
