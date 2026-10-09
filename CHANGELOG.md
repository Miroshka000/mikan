# Changelog

Each release has a section in English and in Russian; the release workflow puts them in
the signed manifest, and the panel shows the one in its language.

## 0.5.0.5
### en
- Platega payments stayed "awaiting payment" although the buyer had paid: once the buyer picks a method, Platega adds its fee on top (5% for SBP), and the panel refused a paid amount that differed from the invoice. A payment that covers the invoice now counts; less, or another currency, is still refused. Payments that expired while awaiting have to be given by hand.
- When the panel's own check of an open invoice fails (the provider is down, the keys stopped working), it now says so in its log.
- "Telegram through a node" could not be saved without a bot token: the check followed Telegram's redirect to core.telegram.org, which a node does not open a way to, and reported "Telegram does not answer this way". Any answer of the Bot API now passes. When a way to Telegram does fail, the panel's log says why (`telegram: route check failed`).

### ru
- Платежи через Platega висели в «Ждёт оплаты», хотя покупатель заплатил: после выбора способа Platega добавляет свою комиссию сверху (5% для СБП), а панель не принимала сумму, отличную от счёта. Теперь оплата, покрывающая счёт, засчитывается; меньше или в другой валюте по-прежнему отклоняется. Платежи, которые уже истекли в ожидании, нужно выдать вручную.
- Если собственная проверка открытого счёта не удалась (провайдер недоступен, ключи перестали работать), панель теперь пишет это в лог.
- «Связь с Telegram через ноду» не сохранялась без токена бота: проверка шла за редиректом Telegram на core.telegram.org, куда нода путь не открывает, и сообщала «Этим путём Telegram не отвечает». Теперь достаточно любого ответа Bot API. Если путь к Telegram и правда не работает, причина есть в логе панели (`telegram: route check failed`).

## 0.5.0.4
### en
- With a domain, Hysteria2, TUIC, AnyTLS and TrustTunnel on the panel's own node failed their handshakes after the update to 0.5.0.3: the links no longer pinned a certificate, while the node could keep serving the self-signed one. The node now starts with the panel's public certificate and gets a new one as soon as it is issued, renewed or uploaded.
- **Lighter on the server**: PostgreSQL is asked far less often while nobody touches the panel. Traffic is stored every 10 seconds instead of every 2 (nothing is lost: the node holds what is not stored yet), "online" and the devices' last seen every 2 minutes, and the torrent blocker no longer rereads every user and slot every 5 seconds. The infrastructure alerts and the settings reload read through a short cache.
- **Lighter in the browser**: an open overview asks the server about twice as rarely, the menu no longer polls the overview's figures on every page, the "Needs attention" card is one request instead of three, and the backdrop stops drifting when the window is not in focus or nobody has touched it for a minute.
- Autotune has three more ports to move a blocked protocol to: 10443, 11443 and 12443. With almost every protocol on one node, the twelve before could all be taken, and a blocked protocol stayed where it was. `mikan update` opens the ports of the list in ufw on servers where ufw is on.
- On Android (with reduced animations or the battery saver on) confirmation windows, such as "Update mikan", slid off to the right edge of the screen; so did the bar of the actions on selected clients. Both now stay in the middle.
- In the dark theme of the subscription page and the Mini App, a plan you touched turned white with unreadable text. It is now a darker shade, and the highlight under the finger no longer sticks on phones.
- The node card's CPU is the whole server's (on the panel's own server the panel and its database count too): it is now called "Server CPU", with the node's own share under it.

### ru
- С доменом Hysteria2, TUIC, AnyTLS и TrustTunnel на своей ноде панели после обновления до 0.5.0.3 обрывали рукопожатие: ссылки больше не закрепляли сертификат, а нода могла остаться на самоподписанном. Теперь нода сразу получает публичный сертификат панели, а новый получает, как только он выдан, продлён или загружен.
- **Меньше нагрузки на сервер**: пока в панели никто ничего не делает, PostgreSQL почти не дёргается. Трафик сохраняется раз в 10 секунд вместо 2 (ничего не теряется: нода держит то, что ещё не сохранено), «в сети» и последнее появление устройств раз в 2 минуты, а блокировка торрентов больше не перечитывает всех пользователей и слоты каждые 5 секунд. Уведомления об инфраструктуре и перечитка настроек идут через короткий кэш.
- **Меньше нагрузки в браузере**: открытый обзор спрашивает сервер примерно вдвое реже, меню больше не опрашивает цифры обзора на каждой странице, карточка «Требует внимания» делает один запрос вместо трёх, а фон перестаёт двигаться, когда окно не в фокусе или его минуту не трогали.
- У автонастройки три новых порта, куда она переносит заблокированный протокол: 10443, 11443 и 12443. Когда на одной ноде включены почти все протоколы, прежние двенадцать могли быть заняты, и заблокированный протокол оставался на месте. `mikan update` открывает порты этого списка в ufw на серверах, где ufw включён.
- На Android (с отключёнными анимациями или в режиме энергосбережения) окна подтверждения, например «Обновить mikan», уезжали к правому краю экрана, как и панель действий с выбранными клиентами. Теперь они по центру.
- В тёмной теме страницы подписки и Mini App тариф, которого коснулись, становился белым с нечитаемым текстом. Теперь он выделяется тёмным оттенком, и подсветка под пальцем на телефоне больше не залипает.
- Процессор на карточке ноды — это весь сервер (на сервере панели туда входят сама панель и её база): теперь он так и называется, «Процессор сервера», а под ним доля самой ноды.

## 0.5.0.3
### en
- **Routing for Clash apps** (Settings → Routing): send services (YouTube, Telegram, Discord, AI, games and more) through the VPN, direct, blocked or through one chosen server; Russian banks, state services and marketplaces past the tunnel; your own DNS; a "blocked only" mode where only what is blocked goes through the VPN. Or write your own Clash profile in YAML: the panel fills in the servers. Profiles are YAML now.
- **Happ** (Settings → Subscription → Happ): a routing profile Happ adds with the subscription, hidden server settings (with your happ-proxy.com Provider ID), and an encrypted link (happ://crypt5) for the Happ button and the user's card, so the subscription address stays hidden.
- **Subscription page builder** (Settings → Subscription page): the page in your brand: palette, light, dark or the visitor's, your colour, background, font and corners; a logo or an emoji; the blocks in your order, turned on or off, with your headings, your text and your buttons; which apps to offer on each platform; your own CSS. With a live preview for phones and desktops, the browser and the Mini App.
- **Instructions** on the subscription page and in the Mini App: pages you write in Markdown, like "How to connect an iPhone".
- **Masking for XHTTP** (Protocols → a protocol → Masking): padding and its obfuscation, the upload method and sizes, and how apps reuse connections, with ready sets. The strong options need recent apps.
- **Users**: folders, the source of each user (created in the panel, bought in the bot, trial, import) and hidden users, all as filters on the Users page. **Traffic per node** on the node cards, in a chart and on the overview.
- With a domain, the panel's own node serves the public certificate on Hysteria2, TUIC, AnyTLS and TrustTunnel, without a pin. Apps that have not refreshed the subscription lose these protocols on that node until they refresh it (they do so every hour).
- A new domain gets its certificate at once. A site of your own: files in /opt/mikan/data/panel/www are served on the paths the panel does not use.
- Blocking detection needs devices from two networks before it moves a port.
- With XHTTP chosen in a Clash app, UDP (calls, QUIC) no longer leaves past the tunnel. XHTTP padding and session settings now reach the apps; REALITY over WebSocket is refused.
- **sing-box apps** (SFA, SFI, SFM, SFT) get a whole sing-box config with the panel's routing and DNS. **Happ and INCY** can take a routing profile the panel builds from the Clash routing ("auto" in the Happ settings). Hiddify 4 gets AnyTLS.
- The nodes run mihomo 1.19.32.
- **Project goals**: the features money is being collected for, with their progress, on the updates card (Settings → System, can be hidden) and on the documentation site.
- **A tidier panel**: Settings and Telegram in sections listed beside the content (a list on phones), cards packed two in a row on wide screens, the page header and the save bar stay in view, the most used things first (the link and QR on top of the client card, a node's rare actions in "⋯", a "Filters" menu for clients). Payments in tabs, with the sales switch in the header. Plainer texts: "Clients" and "Protocols" everywhere.
- **Routing on one page**: rule lists of your own by link (privWL and other community lists), each sent through the VPN, direct, blocked or through a chosen server; how apps pick a server (the fastest or the first that works, how often to check, a group per country or none); fine tuning (block QUIC, read the site name from the connection, real addresses instead of fake-ip). Your Clash rules are on the same page. The menu on the left folds to its icons.
- **Addons** in the menu, in place of Telegram: the Telegram bot and the torrent blocker as tools, with their state, beside the payment methods from the marketplace. The bot keeps working as it did; old links to its page lead to the new one.
- **Traffic filters** (Addons → Traffic filters): the nodes refuse users' traffic to chosen ports, networks and domains, with a preset that closes the mail ports (465, 587, 2525; 25 is closed already), and connections from chosen networks, or let in only those. Nodes older than the panel skip them.
- **VLESS TLS** (Protocols → Add): two new protocols on plain TLS with the real certificate of the panel's domain, like any website, next to REALITY. VLESS · TLS · XHTTP answers strangers as a web server and keeps few connections; VLESS · TLS · Vision works in almost every app, sing-box ones too. A node with a certificate of its own uses that one; other nodes get a self-signed one pinned in the links.
- **A neater panel**: no holes on the overview (the server card is as tall as the chart column, its protocols scroll in it), addons in large cards with what each is set to, the left menu folds smoothly and fits short screens (its sections scroll, the server card no longer slides out of it), and the dark theme is mended: readable avatars, a calmer background, selected tabs and sections that stand out, grey disabled buttons.
- Promo code dates: Chrome no longer turns the year and the hours typed in a row into the year 202600, and a date the panel cannot read is no longer saved silently as none. The same cap on a client's exact end date.

### ru
- **Маршрутизация для Clash-приложений** («Настройки → Маршрутизация»): сервисы (YouTube, Telegram, Discord, нейросети, игры и другие) через VPN, напрямую, в блок или через выбранный сервер; российские банки, госуслуги и маркетплейсы мимо туннеля; свои DNS; режим «только заблокированное», где через VPN идёт только то, что заблокировано. Или свой профиль Clash в YAML: серверы панель подставит сама. Профили теперь в YAML.
- **Happ** («Настройки → Подписка → Happ»): профиль маршрутизации, который Happ добавит вместе с подпиской, скрытые настройки серверов (с вашим Provider ID с happ-proxy.com) и шифрованная ссылка (happ://crypt5) для кнопки Happ и карточки пользователя: адрес подписки не виден.
- **Конструктор страницы подписки** («Настройки → Страница подписки»): страница в вашем стиле: палитра, светлая, тёмная или как у посетителя, ваш цвет, фон, шрифт и скругления; логотип или эмодзи; блоки в вашем порядке, включённые или выключенные, с вашими заголовками, своим текстом и кнопками; какие приложения предлагать на каждой платформе; свой CSS. С живым предпросмотром для телефона и компьютера, браузера и Mini App.
- **Инструкции** на странице подписки и в Mini App: страницы, которые вы пишете в Markdown, например «Как подключить iPhone».
- **Маскировка для XHTTP** («Подключения → подключение → Маскировка»): паддинг и его обфускация, метод и размеры отправки, переиспользование соединений в приложениях, готовые наборы. Сильные настройки требуют свежих приложений.
- **Пользователи**: папки, источник каждого пользователя (создан в панели, куплен в боте, пробный, импорт) и скрытые пользователи, всё фильтрами на странице «Пользователи». **Трафик по нодам** на карточках нод, на графике и в обзоре.
- С доменом своя нода панели отдаёт публичный сертификат на Hysteria2, TUIC, AnyTLS и TrustTunnel, без закрепления. Приложения, которые ещё не обновили подписку, теряют эти протоколы на этой ноде до обновления (оно идёт раз в час).
- Новый домен получает сертификат сразу. Свой сайт: файлы из /opt/mikan/data/panel/www отдаются на путях, которые не заняты панелью.
- Обнаружение блокировок переносит порт, только когда не доходят устройства хотя бы из двух сетей.
- С выбранным XHTTP в Clash-приложении UDP (звонки, QUIC) больше не уходит мимо туннеля. Настройки паддинга и сессий XHTTP теперь доходят до приложений; REALITY поверх WebSocket не принимается.
- **Приложения sing-box** (SFA, SFI, SFM, SFT) получают целый конфиг sing-box с маршрутизацией и DNS панели. **Happ и INCY** могут брать профиль маршрутизации, который панель собирает из маршрутизации Clash («auto» в настройках Happ). Hiddify 4 получает AnyTLS.
- Ноды работают на mihomo 1.19.32.
- **Цели проекта**: на какие функции идёт сбор денег и сколько собрано. Видно в карточке обновлений («Настройки → Система», можно скрыть) и на сайте документации.
- **Панель удобнее**: Настройки и Telegram разбиты на разделы со списком сбоку (на телефоне выпадающий список), на широком экране карточки стоят по две в ряд, шапка страницы и кнопка сохранения всегда на виду, самое нужное наверху (ссылка и QR первыми в карточке клиента, редкие действия ноды в «⋯», меню «Фильтры» у клиентов). Платежи во вкладках, тумблер продажи в шапке. Тексты проще: «Клиенты» и «Протоколы» везде.
- **Маршрутизация на одной странице**: свои списки правил по ссылке (privWL и другие списки сообщества), каждый через VPN, напрямую, в блок или через выбранный сервер; как приложения выбирают сервер (самый быстрый или первый рабочий, как часто проверять, группа на страну или без неё); тонкая настройка (блок QUIC, определение сайта по соединению, настоящие адреса вместо fake-ip). Ваши правила Clash на той же странице. Меню слева сворачивается до значков.
- **Аддоны** в меню вместо Telegram: Telegram-бот и блокировка торрентов как инструменты, с их состоянием, рядом со способами оплаты из маркетплейса. Бот работает как работал; старые ссылки на его страницу ведут на новую.
- **Фильтры трафика** («Аддоны → Фильтры трафика»): ноды не пропускают трафик пользователей на выбранные порты, сети и домены, с пресетом, закрывающим почтовые порты (465, 587, 2525; 25 закрыт и так), и не пускают подключения из выбранных сетей или пускают только из них. Ноды старше панели их пропускают.
- **VLESS TLS** («Протоколы → Добавить»): два новых протокола на обычном TLS с настоящим сертификатом домена панели, как у любого сайта, рядом с REALITY. VLESS · TLS · XHTTP на чужие запросы отвечает как веб-сервер и держит мало соединений; VLESS · TLS · Vision работает почти во всех приложениях, и в sing-box тоже. Нода со своим сертификатом использует его, остальные ноды получают самоподписанный с пином в ссылках.
- **Панель аккуратнее**: на обзоре нет пустот (карточка сервера по высоте колонки с графиком, протоколы прокручиваются внутри), аддоны в больших карточках с тем, как каждый настроен, меню слева сворачивается плавно и помещается на невысоком экране (разделы прокручиваются, карточка сервера больше не съезжает за край), тёмная тема поправлена: видны инициалы на аватарах, фон спокойнее, выбранные вкладки и разделы заметны, неактивные кнопки серые.
- Даты промокода: Chrome больше не склеивает год и часы, набранные подряд, в год 202600, а дата, которую панель не может прочитать, больше не сохраняется молча как пустая. То же ограничение у точной даты окончания клиента.

## 0.5.0.2
### en
- **Nodes update from the panel.** The Nodes page shows each node's version and marks the ones behind the panel, with **Update** and **Update all**. By default nodes follow the panel by themselves: after the panel updates, they are brought to its version one at a time, the next one starting when the previous one works. A node that fails goes back to its version, you get a notice, and the rollout stops. The switch is in Settings → General → Updates.
- A node installs only signed releases and never goes back to an older one. Its nightly timer no longer moves it ahead of the panel.
- Nodes on 0.5.0.1 and older need `mikan update` on their server once; the panel shows the command. After that they update from the panel.
- With device binding, the places taken are the bound devices: a phone moving from Wi-Fi to mobile data no longer looks like two devices. Connection addresses are shown separately and take no places.
- "What changed" in Settings → Updates shows bold text, code and links properly.
- **Torrent blocker** (Settings → Clash rules): nodes recognise BitTorrent and drop it; a caught user can be banned on every node for a chosen time. The ban is per user, not per IP. A plain tracker request alone bans nobody: it takes three within ten minutes, since a web page can make a browser send one. Users can be exempted, and bans lifted in their card.
- **Speed test of a node** (Nodes → a node): latency, loss, download and upload, with the history. One test uses up to 250 MB down and 100 MB up; a node can be tested once in 5 minutes.
- **Subscription name in apps** (Settings → Subscription): the profile name Happ, v2RayTun and Hiddify show, with variables like `{name}`, `{days}` and `{left}`; the announcement takes them too.
- **Close a traffic pool on a plan**: the plan's users lose that pool at once, and its packages are not sold to them. Opening it again puts the plan's limit back on them.
- **Remove a subscription from the bot**: the subscription screen in the bot has "Remove from the bot". Only the Telegram link goes: the subscription keeps working in the apps and comes back when its link is sent to the bot.
- **Server order** (Nodes, arrows up and down): the order of the nodes is the order of the servers in subscriptions, in every app.
- Promo codes: the bot's **Promo codes** button opens the Mini App signed in, one **Apply** takes both bonus and discount codes (a discount applies at checkout), and discounts are shown in rubles. In the promo code form sums of a RUB code are entered in rubles; a minimum order or a maximum discount needs the code's currency.
- A Telegram Stars refund takes back what the payment gave: a new subscription is turned off, a renewal loses its term (and gets its previous plan back when nothing changed since), a traffic package is removed, and the promo code can be used again. Refunds Telegram makes itself do the same. The buyer gets a message in the bot.
- The bot takes the old subscription links of users imported from Marzban, PasarGuard or Remnawave, and the user's card shows the old link (Remnawave) or that it still works.
- Cascade: the relay on the exit node no longer takes a port another program holds, and a relay whose port is taken moves to a free one by itself. The cascade window shows the relay's state, and an alert comes when it is down. Works with nodes on 0.5.0.2.
- WARP: the WARP window shows why the check fails (no UDP answer from the endpoint, DNS, TLS and so on), the node writes it to its log, and **Check** really checks again. The WARP alert in Telegram gives the reason too.
- Behind a proxy (a protocol with its own listen address) the masking site no longer changes by itself, like the port: a proxy that routes by the site's name would lose the clients. The update turns it off where it was on.
- Clash apps (Koala Clash, Clash Verge, FlClash) name the profile with the subscription name from Settings → Subscription instead of the brand.

### ru
- **Ноды обновляются из панели.** На странице «Ноды» видна версия каждой ноды и отмечены отстающие от панели, есть кнопки **Обновить** и **Обновить все**. По умолчанию ноды сами следуют за панелью: после её обновления они подтягиваются до её версии по одной, следующая начинает, когда предыдущая заработала. Нода, которая не обновилась, возвращается на свою версию, приходит уведомление, и обновление останавливается. Переключатель в «Настройки → Основное → Обновления».
- Нода ставит только подписанные релизы и никогда не откатывается на старую версию. Её ночной таймер больше не обгоняет панель.
- Нодам на 0.5.0.1 и раньше один раз нужен `mikan update` на их сервере, панель показывает команду. Дальше они обновляются из панели.
- С привязкой устройств места считаются по привязанным устройствам: телефон, перешедший с Wi-Fi на мобильную сеть, больше не выглядит как два устройства. Адреса подключений показаны отдельно и мест не занимают.
- «Что изменилось» в «Настройки → Обновления» показывает жирный текст, код и ссылки как надо.
- **Блокировка торрентов** («Настройки → Правила Clash»): ноды узнают BitTorrent и не пропускают его, пойманного пользователя можно забанить на всех нодах на выбранное время. Бан вешается на пользователя, а не на IP. Один простой запрос к трекеру никого не банит: нужно три за десять минут, ведь такой запрос может отправить и обычная веб-страница. Пользователей можно исключить, а бан снять в их карточке.
- **Проверка скорости ноды** («Ноды → нода»): задержка, потери, загрузка и отдача, с историей. Один тест расходует до 250 МБ на загрузку и 100 МБ на отдачу, ноду можно проверять раз в 5 минут.
- **Название подписки в приложениях** («Настройки → Подписка»): имя профиля, которое показывают Happ, v2RayTun и Hiddify, с переменными вроде `{name}`, `{days}` и `{left}`, объявление тоже их понимает.
- **Закрыть пул трафика на тарифе**: пользователи тарифа сразу теряют этот пул, пакеты для него им не продаются. При открытии им снова ставится лимит тарифа.
- **Убрать подписку из бота**: на экране подписки в боте есть кнопка «Убрать из бота». Снимается только привязка к Telegram: подписка продолжает работать в приложениях и возвращается в бот, если прислать ему её ссылку.
- **Порядок серверов** («Ноды», стрелки вверх и вниз): в каком порядке стоят ноды, в таком порядке серверы идут в подписке, во всех приложениях.
- Промокоды: кнопка **Промокоды** в боте открывает Mini App со входом, одна кнопка **Применить** принимает и бонусные, и скидочные коды (скидка применяется при оплате), скидки показываются в рублях. В форме промокода суммы для кода в RUB вводятся в рублях, а минимальная сумма или максимальная скидка требуют выбрать валюту кода.
- Возврат Telegram Stars забирает то, что дал платёж: новая подписка выключается, продление теряет свой срок (и получает прежний тариф, если с тех пор ничего не менялось), пакет трафика снимается, промокод снова можно использовать. Возвраты, которые Telegram делает сам, работают так же. Покупатель получает сообщение в боте.
- Бот принимает старые ссылки подписок пользователей, перенесённых из Marzban, PasarGuard или Remnawave, а в карточке пользователя видна старая ссылка (Remnawave) или отметка, что она работает.
- Каскад: служебный вход на ноде выхода больше не занимает порт, который держит другая программа, а если порт заняли, сам переезжает на свободный. В окне каскада видно его состояние, при сбое приходит уведомление. Работает с нодами на 0.5.0.2.
- WARP: окно WARP показывает, почему проверка не проходит (нет ответа от endpoint по UDP, DNS, TLS и так далее), нода пишет причину в свой лог, а кнопка **Проверить** действительно проверяет заново. Уведомление о WARP в Telegram тоже называет причину.
- За прокси (у протокола свой адрес для прослушивания) сайт маскировки больше не меняется сам, как и порт: прокси, который выбирает подключение по имени сайта, потерял бы клиентов. Обновление выключает это там, где было включено.
- Clash-приложения (Koala Clash, Clash Verge, FlClash) называют профиль названием подписки из «Настройки → Подписка», а не брендом.

## 0.5.0.1
### en
- The installer no longer stops when nginx or Caddy holds ports 80 or 443. The protocols whose port is taken get other free ports. With a domain, it offers to add the Let's Encrypt rule to nginx or Caddy: it backs up the config, checks it and rolls back on an error. Without your consent it only shows the lines to add.
- A protocol whose port is held by another program moves to a free port by itself, on the panel's server and on nodes. You get a notice in Telegram, and subscriptions get the new port.
- Installer screen: long links wrap instead of being cut, and output from other programs no longer stays on the screen. After you finish, the full link and login are printed to copy. The password is shown on the last screen only; get a new one with `mikan reset-password`.
- Updates no longer depend on GitHub's "latest" release: servers read a signed list of releases. When a version cannot be reached directly, they go through the one in between by themselves. Settings → General has a switch for beta versions.
- **Several terms in one plan**: a plan can be sold for 7, 30 or 90 days, each with its own price in Stars and rubles (Plans → a plan → More terms). The buyer picks the term in the bot and the Mini App; limits are not multiplied, traffic resets by the plan's strategy. An invoice keeps the term and price it was made for.
- **Free trial in the bot**: pick a plan in Payments → settings, and the bot's welcome offers it once per Telegram account to people without a subscription or payments. It needs a plan with a term.
- Confirmation dialogs are no longer cut off on phones.

### ru
- Установщик больше не останавливается, если порты 80 или 443 заняты nginx или Caddy. Протоколы с занятым портом получают другие свободные порты. С доменом он предлагает сам добавить правило для Let's Encrypt в nginx или Caddy: делает копию конфига, проверяет его и откатывает при ошибке. Без вашего согласия только показывает, какие строки добавить.
- Протокол, чей порт заняла другая программа, сам переезжает на свободный порт, и на сервере панели, и на нодах. Приходит уведомление в Telegram, подписки получают новый порт.
- Экран установщика: длинные ссылки переносятся, а не обрезаются, вывод других программ больше не остаётся на экране. После завершения полная ссылка и логин печатаются для копирования. Пароль показывается только на последнем экране, новый можно получить командой `mikan reset-password`.
- Обновления больше не зависят от релиза «latest» на GitHub: серверы читают подписанный список релизов. Если до версии нельзя дойти напрямую, они сами проходят через промежуточную. В «Настройки → Основное» появился переключатель бета-версий.
- **Несколько сроков в одном тарифе**: тариф можно продавать на 7, 30 или 90 дней, у каждого срока своя цена в Stars и рублях («Тарифы → тариф → Другие сроки»). Покупатель выбирает срок в боте и Mini App, лимиты не умножаются, трафик сбрасывается по стратегии тарифа. Счёт сохраняет срок и цену, с которыми выставлен.
- **Пробный период в боте**: выберите тариф в «Платежи → настройки», и приветствие бота предложит его один раз на Telegram-аккаунт тем, у кого нет подписки и оплат. Нужен тариф со сроком.
- Диалоги подтверждения больше не обрезаются на телефонах.

## 0.5.0.0
### en
- The panel now uses PostgreSQL. The update moves all data over: subscription links, VPN keys, user IDs, counters, payments and settings stay the same. A backup is made first, and the old SQLite database stays on disk. New backups are PostgreSQL archives; old SQLite backups can still be restored.
- How to get it: on 0.4.4 or older, update to 0.4.5 first with the **Update** button (or `mikan update`). After that 0.5.0.0 arrives the usual way, by the **Update** button or the nightly auto-update.
- If the move to PostgreSQL fails, the panel stays stopped and keeps both databases; run the update again.
- Versions now have four numbers: major.minor.patch.revision.
- From 0.5 on, an update that does not come up goes back to the previous version by itself when it did not change the database schema; otherwise the panel stays stopped with its data for another try. Restoring a backup replaces the whole database.
- **Promo codes**, a new page: bonus days, extra traffic into the main limit or a traffic pool, and percent or fixed discounts on plans and traffic packages. Limits by dates, total uses, uses per person, new users or first purchase only, and chosen plans. Buyers enter a code in the Mini App; every activation is kept in the history.
- **Import from another panel**: Settings → Import moves users from Marzban, PasarGuard or Remnawave onto a chosen plan, with their traffic used, term, device limit and note. A check shows what will happen first; the import runs in the background. The old subscription links keep working once the old domain points to this panel.
- **Server alerts in Telegram** (Telegram → Infrastructure): private notices to the admin about nodes, WARP, cascade exits, protocols, autotune, the TLS certificate and updates, and a public server status in a channel. A node's public name is set in its settings; without one the node is not shown.
- **Daily database backups to the admin's Telegram chat**, encrypted with your password, at the hour you choose (Telegram → Infrastructure).
- **Five themes**: Mikan, Midnight, Ocean, Sakura and Forest, in Settings → General. The choice is kept in the browser.
- **Subscription page and apps**: a Linux tab (SlothClash, Clash Verge Rev, Hiddify), ClashFest and SlothClash among the apps, an announcement shown inside Happ, v2RayTun and ClashFest, and your brand in ClashFest and SlothClash (Settings → Subscription).
- **Prometheus metrics** at `/api/v1/metrics` with a read key; the API page (Settings → Security) shows a ready scrape job.
- A new logo: the painted mandarin in the panel, the browser tab and on the subscription page.

### ru
- Панель переходит на PostgreSQL. Обновление переносит все данные: ссылки подписок, VPN-ключи, ID пользователей, счётчики, платежи и настройки не меняются. Перед переносом делается бэкап, старая база SQLite остаётся на диске. Новые бэкапы — архивы PostgreSQL, старые SQLite-бэкапы по-прежнему можно восстановить.
- Как обновиться: на 0.4.4 и более ранних версиях сначала обновитесь до 0.4.5 кнопкой **Обновить** (или `mikan update`). После этого 0.5.0.0 придёт как обычно — кнопкой **Обновить** или ночным автообновлением.
- Если перенос на PostgreSQL не удался, панель остаётся остановленной и сохраняет обе базы; запустите обновление ещё раз.
- Версии теперь из четырёх чисел: major.minor.patch.revision.
- Начиная с 0.5 обновление, которое не поднялось, само откатывается на прежнюю версию, если не меняло схему базы; иначе панель остаётся остановленной с данными для повторной попытки. Восстановление бэкапа заменяет базу целиком.
- **Промокоды**, новая страница: бонусные дни, дополнительный трафик в основной лимит или в пул трафика, скидки в процентах или фиксированной суммой на тарифы и пакеты трафика. Ограничения по датам, общему числу активаций, активациям на человека, только для новых или для первой покупки и по тарифам. Покупатель вводит код в Mini App, все активации видны в истории.
- **Импорт из другой панели**: «Настройки → Импорт» переносит пользователей из Marzban, PasarGuard или Remnawave на выбранный тариф вместе с израсходованным трафиком, сроком, лимитом устройств и заметкой. Сначала проверка показывает, что будет, импорт идёт в фоне. Старые ссылки подписок продолжают работать, когда старый домен направлен на эту панель.
- **Уведомления о серверах в Telegram** («Telegram → Инфраструктура»): личные сообщения админу о нодах, WARP, выходах каскадов, протоколах, autotune, TLS-сертификате и обновлениях, и публичный статус серверов в канале. Публичное имя ноды задаётся в её настройках, без него нода в статусе не показывается.
- **Ежедневные бэкапы базы в Telegram-чат админа**, зашифрованные вашим паролем, в выбранный час («Telegram → Инфраструктура»).
- **Пять тем оформления**: Mikan, Midnight, Ocean, Sakura и Forest, в «Настройки → Основное». Выбор сохраняется в браузере.
- **Страница подписки и приложения**: вкладка Linux (SlothClash, Clash Verge Rev, Hiddify), ClashFest и SlothClash в списке приложений, объявление внутри Happ, v2RayTun и ClashFest и ваш бренд в ClashFest и SlothClash («Настройки → Подписка»).
- **Метрики Prometheus** на `/api/v1/metrics` по ключу на чтение; на странице API («Настройки → Безопасность») есть готовый job для сбора.
- Новый логотип: нарисованный мандарин в панели, на вкладке браузера и на странице подписки.

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
