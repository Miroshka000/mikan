// What the random site is made of: for each kind of small site, in each language, the names,
// the words of the first screen and pools the sections draw from. Nothing here claims a fact
// about a real company: no reviews, no counts of clients, no addresses or phones.

export type Lang = "ru" | "en";
export type Kind = "notes" | "tool" | "recipes" | "club" | "studio" | "gallery";
export const KINDS: Kind[] = ["notes", "tool", "recipes", "club", "studio", "gallery"];

export type ListStyle = "rows" | "cards" | "steps";
/** title, text, a short note beside the title (a date, a time, a level) */
export type Item = [string, string, string?];
/** name, note, value */
export type Row = [string, string, string];

export type SectionBank =
  | { t: "list"; title: string[]; style: ListStyle; take: [number, number]; pool: Item[]; dated?: boolean }
  | { t: "prose"; title: string[]; take: number; pool: string[] }
  | { t: "table"; title: string[]; take: [number, number]; pool: Row[] }
  | { t: "gallery"; title: string[]; take: [number, number]; pool: string[] }
  | { t: "code"; title: string[]; pool: string[][] }
  | { t: "facts"; title: string[]; take: [number, number]; pool: [string, string][] };

export type Bank = {
  /** Whole names, or two lists to join one of each from (a tool's name). */
  names: string[] | [string[], string[]];
  tags: string[];
  heroes: [string, string][];
  foot: string[];
  sections: SectionBank[];
};

export const MONTHS: Record<Lang, string[]> = {
  ru: ["января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"],
  en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
};

const TOOL_NAMES: [string[], string[]] = [
  ["path", "log", "tab", "glob", "line", "diff", "cron", "env", "csv", "note"],
  ["sift", "trim", "pick", "fold", "peek", "sort", "mend", "tidy"],
];

export const BANKS: Record<Lang, Record<Kind, Bank>> = {
  ru: {
    notes: {
      names: ["Поля тетради", "Медленный текст", "Вторая чашка", "Северный подоконник", "Записки на полях", "Карандаш и карта", "Длинная дорога домой"],
      tags: ["Заметки о городе, книгах и пеших прогулках", "Пишу редко и не по расписанию", "Дневник одного любопытного человека"],
      heroes: [
        ["Заметки, которые я пишу для себя и немного для вас", "Здесь нет рубрик и расписания. Когда что-то кажется достойным пары абзацев, оно попадает сюда: прогулка, прочитанная книга, удачный рецепт или неудачный ремонт."],
        ["Пишу о том, что успел заметить по дороге", "Короткие тексты без повода. Про дворы, вокзалы, старые карты и про то, как устроены обычные вещи."],
        ["Блог без новостей", "Никаких подборок и прогнозов. Только то, что я видел сам, с датой и без выводов."],
      ],
      foot: ["Тексты можно цитировать со ссылкой.", "Написано вручную, без спешки.", "Обновляется, когда есть о чём написать."],
      sections: [
        { t: "list", title: ["Последние записи", "Недавнее", "Что нового"], style: "rows", take: [4, 6], dated: true, pool: [
          ["Трамвай номер семь", "Проехал маршрут от кольца до кольца и записал всё, что видно из окна: три депо, один рынок и мост, под которым зимуют утки."],
          ["Как я чинил табуретку", "Четыре вечера, два вида клея и один вывод: шкант надёжнее самореза, если не торопиться."],
          ["Список книг за осень", "Семь названий, из которых я бы перечитал два. Про остальные рассказываю коротко и честно."],
          ["Двор с голубятней", "В соседнем квартале до сих пор держат голубей. Поговорил с хозяином и узнал, почему птицы всегда возвращаются."],
          ["Чай из термоса", "Заметка о том, почему на холоде обычный чёрный чай вкуснее любого редкого сорта."],
          ["Старая карта района", "Нашёл план сорокалетней давности и прошёл по улицам, которых на новой карте уже нет."],
          ["Про почерк", "Месяц писал от руки по странице в день. Буквы ровнее не стали, зато мысли стали короче."],
          ["Вокзальные часы", "На нашем вокзале трое часов, и все показывают разное время. Выяснил, какие правы."],
        ] },
        { t: "prose", title: ["Об авторе", "Кто это пишет", "Пара слов о себе"], take: 2, pool: [
          "Я работаю с текстами и чертежами, а в свободное время хожу пешком. За год набирается около тысячи километров, большая часть по одному и тому же городу.",
          "Блог я завёл, чтобы не терять заметки из бумажных тетрадей. Часть записей переписана оттуда почти без правки.",
          "Фотографий здесь мало: я плохо снимаю и хорошо запоминаю. Вместо снимков иногда рисую схемы.",
          "Комментариев нет. Если хочется ответить, напишите письмо: я читаю всё, отвечаю не сразу.",
        ] },
        { t: "list", title: ["Темы", "О чём здесь пишут", "Разделы без строгих границ"], style: "cards", take: [3, 4], pool: [
          ["Прогулки", "Маршруты по городу и окрестностям, с расстояниями и местами, где можно сесть."],
          ["Чтение", "Короткие отзывы на книги, которые я дочитал. Недочитанные тоже упоминаю."],
          ["Починка", "Мебель, велосипед, кран на кухне. Что получилось, что нет и сколько ушло времени."],
          ["Кухня", "Простая еда на каждый день: каши, супы и то, что пекут в выходные."],
          ["Карты", "Старые планы, схемы транспорта и собственные наброски."],
        ] },
      ],
    },
    tool: {
      names: TOOL_NAMES,
      tags: ["Маленькая утилита командной строки", "Одна задача, один исполняемый файл", "Инструмент для терминала, без зависимостей"],
      heroes: [
        ["{name} находит лишнее в больших текстовых файлах", "Читает поток построчно, не загружает файл в память и отдаёт результат в обычный вывод. Работает в конвейере с другими командами."],
        ["{name}: фильтр строк, который не нужно настраивать", "Никаких конфигов и плагинов. Один файл, понятные флаги, справка на одном экране."],
        ["{name} приводит журналы в читаемый вид", "Склеивает перенесённые строки, убирает повторы и выравнивает время. Вход и выход: простой текст."],
      ],
      foot: ["Исходный код открыт, лицензия MIT.", "Собрано без внешних библиотек.", "Сообщения об ошибках принимаются в виде минимального примера."],
      sections: [
        { t: "code", title: ["Установка и первый запуск", "Быстрый старт", "Как запустить"], pool: [
          ["$ curl -LO https://example.org/{name}.tar.gz", "$ tar -xzf {name}.tar.gz", "$ ./{name} --help", "", "$ cat app.log | ./{name} --since 1h --level warn"],
          ["$ make", "$ sudo make install", "", "$ {name} -n 20 access.log", "$ {name} --count '*.csv'"],
          ["$ go install example.org/{name}@latest", "", "$ {name} input.txt > clean.txt", "$ tail -f server.log | {name} --follow"],
        ] },
        { t: "list", title: ["Что умеет", "Возможности", "Для чего это"], style: "cards", take: [3, 5], pool: [
          ["Потоковая обработка", "Файл на десять гигабайт читается так же, как на десять килобайт: память не растёт."],
          ["Понятные флаги", "Каждый флаг есть в короткой и длинной форме, значения по умолчанию написаны в справке."],
          ["Коды возврата", "Ноль, если что-то найдено, единица, если нет, двойка при ошибке. Удобно в скриптах."],
          ["Без цвета по умолчанию", "Цвет включается только в терминале. В конвейере вывод всегда чистый."],
          ["Кодировки", "UTF-8 и однобайтные кодировки определяются сами, остальное задаётся флагом."],
          ["Один файл", "Статическая сборка: скопируйте на сервер, и больше ничего ставить не нужно."],
        ] },
        { t: "table", title: ["История версий", "Что менялось", "Журнал изменений"], take: [3, 5], pool: [
          ["0.9.2", "Исправлена обработка пустых строк в конце файла", "март"],
          ["0.9.0", "Флаг --follow для чтения растущего файла", "январь"],
          ["0.8.1", "Справка переписана, примеры перенесены в начало", "ноябрь"],
          ["0.8.0", "Чтение нескольких файлов за один запуск", "сентябрь"],
          ["0.7.3", "Сборка под ARM, размер файла уменьшен на треть", "июль"],
          ["0.7.0", "Первая версия с тестами на больших файлах", "май"],
        ] },
        { t: "prose", title: ["Зачем ещё одна утилита", "Откуда это взялось", "Почему так"], take: 2, pool: [
          "Программа выросла из сценария на двадцать строк, который я копировал с сервера на сервер. В какой-то момент проще стало переписать его как следует.",
          "Я намеренно не добавляю новые режимы. Если задачу можно решить соседней стандартной командой, здесь её не будет.",
          "Тесты гоняются на файлах из настоящих журналов, очищенных от адресов и имён. Самый большой весит четырнадцать гигабайт.",
          "Обратная совместимость флагов сохраняется с версии 0.7. Всё, что уберу, сначала год будет выдавать предупреждение.",
        ] },
      ],
    },
    recipes: {
      names: ["Чугунная сковорода", "Кухонная тетрадь", "Соль и огонь", "Суп по средам", "Тёплая плита", "Половник", "Ложка мёда"],
      tags: ["Домашние рецепты, проверенные на своей кухне", "Простая еда на каждый день", "Тетрадь рецептов без лишних слов"],
      heroes: [
        ["Рецепты, которые получаются с первого раза", "Каждое блюдо я готовил не меньше трёх раз, прежде чем записать. Граммы и минуты указаны точно, редких продуктов нет."],
        ["Готовим дома: суп, хлеб и что-нибудь к чаю", "Здесь собрано то, что едят в обычные дни. Без ресторанной подачи, зато с понятным списком покупок."],
        ["Кухонная тетрадь, перенесённая в сеть", "Рецепты записаны так, как я диктую их друзьям по телефону: что купить, что делать и когда снимать с огня."],
      ],
      foot: ["Вес продуктов указан до очистки.", "Время дано для обычной электрической плиты.", "Рецепты можно переписывать и менять под себя."],
      sections: [
        { t: "list", title: ["Что приготовить на этой неделе", "Свежие рецепты", "Недавно записано"], style: "cards", take: [4, 6], pool: [
          ["Щи из свежей капусты", "Говяжий бульон, капуста, картофель и ложка томата в самом конце. Настаиваются час под крышкой.", "1 ч 40 мин"],
          ["Гречка с грибами и луком", "Крупа обжаривается на сухой сковороде, грибы отдельно. Соединяются за пять минут до подачи.", "35 мин"],
          ["Пшеничный хлеб на закваске", "Замес вечером, расстойка ночью в холодильнике, выпечка утром в чугунной кастрюле.", "18 ч"],
          ["Сырники без муки", "Сухой творог, желток и манка. Формуются мокрыми руками, жарятся на среднем огне.", "25 мин"],
          ["Томатный суп с фасолью", "Консервированные томаты, белая фасоль, чеснок и сухой хлеб. Готов быстрее, чем закипит чайник дважды.", "30 мин"],
          ["Запечённые яблоки с орехами", "Сердцевина вынимается, внутрь кладутся грецкие орехи и мёд. Сорок минут при ста восьмидесяти.", "50 мин"],
          ["Картофельные драники", "Картофель на мелкой тёрке, лук, яйцо. Сок отжимается, иначе драники получатся серыми.", "40 мин"],
          ["Овсяное печенье с изюмом", "Хлопья долгой варки, масло, сахар, яйцо. Противень вынимается, пока середина ещё мягкая.", "30 мин"],
        ] },
        { t: "table", title: ["Борщ на большую кастрюлю: что купить", "Продукты для воскресного супа", "Список на рынок"], take: [5, 7], pool: [
          ["Говядина на кости", "лопатка или грудинка", "700 г"],
          ["Свёкла", "две средние", "400 г"],
          ["Капуста белокочанная", "четверть кочана", "350 г"],
          ["Картофель", "три клубня", "300 г"],
          ["Морковь и лук", "по одной штуке", "250 г"],
          ["Томатная паста", "столовая ложка с горкой", "40 г"],
          ["Чеснок", "три зубчика", "15 г"],
          ["Уксус яблочный", "чтобы свёкла держала цвет", "1 ст. л."],
        ] },
        { t: "list", title: ["Как читать эти рецепты", "Несколько правил кухни", "Прежде чем начать"], style: "steps", take: [3, 4], pool: [
          ["Сначала прочитайте до конца", "В середине рецепта не должно оказаться «заранее замочите на ночь»."],
          ["Взвесьте и разложите", "Когда всё нарезано и стоит рядом с плитой, блюдо готовится вдвое спокойнее."],
          ["Солите в два приёма", "Половину в начале, половину в конце, после пробы. Пересол исправить труднее, чем недосол."],
          ["Дайте блюду постоять", "Суп, мясо и выпечка после огня доходят ещё десять минут. Не режьте сразу."],
        ] },
        { t: "prose", title: ["О тетради", "Откуда рецепты", "Кто готовит"], take: 2, pool: [
          "Первую тетрадь мне отдала бабушка, в ней было сорок рецептов и ни одного точного веса. Я готовил по ней и записывал граммы.",
          "Я не повар. Готовлю каждый день на семью из четырёх человек и записываю то, что просят повторить.",
          "Сезон здесь важнее новизны: зимой супы и выпечка, летом овощи и то, что не требует духовки.",
          "Если блюдо не получилось, скорее всего дело в плите. Попробуйте убавить огонь и прибавить время.",
        ] },
      ],
    },
    club: {
      names: ["Клуб «Жёлтая фишка»", "Настольные четверги", "Клуб «Шестигранник»", "Игротека на Заречной", "Клуб «Ход конём»", "Клуб «Длинная партия»"],
      tags: ["Настольные игры по вечерам, вход свободный", "Играем каждую неделю, новичков учим за столом", "Клуб любителей настольных игр"],
      heroes: [
        ["Приходите играть: объясним правила и найдём вам стол", "Мы собираемся раз в неделю и раскладываем от шести до десяти игр. Свою приносить не обязательно, опыт тоже не нужен."],
        ["Вечер настольных игр без турниров и взносов", "Обычная компания: кто-то играет двадцать лет, кто-то пришёл впервые. За каждым столом есть человек, который знает правила."],
        ["Садитесь за любой стол", "Партия длится от двадцати минут до трёх часов. Скажите на входе, сколько у вас времени, и мы подберём игру."],
      ],
      foot: ["Игры из общей полки возвращаются в коробку собранными.", "Еду к столам с картами не приносим.", "Клуб существует на добровольные взносы участников."],
      sections: [
        { t: "table", title: ["Расписание на месяц", "Когда играем", "Ближайшие встречи"], take: [4, 5], pool: [
          ["Первый четверг", "семейные игры, партии до часа", "19:00"],
          ["Второй четверг", "стратегии на весь вечер", "18:30"],
          ["Третий четверг", "карточные игры и дуэли", "19:00"],
          ["Четвёртый четверг", "новинки полки, учим правила", "19:00"],
          ["Последняя суббота", "длинные партии с обедом", "12:00"],
          ["Воскресенье через раз", "игры с детьми от восьми лет", "11:00"],
        ] },
        { t: "list", title: ["Что лежит на полке", "Во что играем", "Игры клуба"], style: "cards", take: [4, 6], pool: [
          ["Колонизаторы", "Торговля ресурсами и дороги на острове. Четыре игрока, полтора часа.", "легко начать"],
          ["Каркассон", "Собираем карту из квадратов и занимаем города. Объясняется за пять минут.", "легко начать"],
          ["Билет на поезд", "Строим маршруты между городами. Хороша для первой встречи.", "легко начать"],
          ["Агрикола", "Хозяйство на маленькой ферме, где всегда не хватает еды. Думать придётся много.", "сложнее"],
          ["Шакал", "Пираты ищут клад на острове из перевёрнутых клеток. Громкая и быстрая.", "для компании"],
          ["Шахматы и го", "Две доски стоят у окна постоянно. Часы есть, блиц по желанию.", "вдвоём"],
          ["Кодовые имена", "Две команды и одно слово-подсказка на несколько карт. До двенадцати человек.", "для компании"],
        ] },
        { t: "list", title: ["Как прийти впервые", "Первый вечер", "Если вы у нас не были"], style: "steps", take: [3, 3], pool: [
          ["Придите к началу", "В первые полчаса формируются столы. Позже тоже можно, но придётся ждать конца партии."],
          ["Скажите, во что играли раньше", "Даже если только в лото. От этого зависит, какую игру вам предложат."],
          ["Сыграйте одну партию и решайте", "Никаких анкет и членства. Понравится, приходите через неделю."],
        ] },
        { t: "prose", title: ["Правила клуба", "О нас", "Как всё устроено"], take: 2, pool: [
          "Клуб начался с четырёх друзей и одной коробки. Сейчас на полке больше шестидесяти игр, часть подарили участники.",
          "Мы не продаём игры и не проводим платных мероприятий. Чай и печенье покупаем в складчину.",
          "Главное правило одно: проигрывать спокойно. За столом не подсказывают, если об этом не просят.",
          "Игру можно взять домой на неделю, записав своё имя в тетрадь у полки.",
        ] },
      ],
    },
    studio: {
      names: ["Мастерская «Обожжённая глина»", "Гончарная на втором этаже", "Мастерская «Круг и печь»", "Студия керамики «Черепок»", "Мастерская «Серая глина»"],
      tags: ["Керамическая мастерская: занятия и свободная лепка", "Гончарный круг, глазури и печь на восемьдесят литров", "Небольшая студия керамики"],
      heroes: [
        ["Слепите чашку своими руками и заберите её через две недели", "На первом занятии вы сядете за круг и сделаете простую форму. Мы высушим, обожжём и покроем её глазурью, которую вы выберете."],
        ["Гончарный круг для тех, кто ни разу не пробовал", "Группы до пяти человек, у каждого свой круг. Глина, инструменты и фартук уже здесь."],
        ["Мастерская, где можно работать с глиной в своём темпе", "Занятия с мастером для начинающих и свободные часы для тех, кто уже умеет центровать."],
      ],
      foot: ["Обжиг и глазурь входят в стоимость занятия.", "Готовые изделия хранятся месяц.", "Работаем с глиной без примесей свинца."],
      sections: [
        { t: "table", title: ["Занятия и цены", "Что можно выбрать", "Форматы"], take: [3, 5], pool: [
          ["Первое занятие на круге", "2 часа, одна вещь", "2 400 ₽"],
          ["Курс из четырёх занятий", "круг, ручная лепка, глазурь", "8 000 ₽"],
          ["Свободная мастерская", "час работы без мастера", "600 ₽"],
          ["Занятие вдвоём", "два круга рядом, 2 часа", "4 200 ₽"],
          ["Лепка с детьми", "от шести лет, 1,5 часа", "1 800 ₽"],
          ["Обжиг своих работ", "за литр объёма печи", "150 ₽"],
        ] },
        { t: "list", title: ["Как проходит работа", "От комка до чашки", "Что происходит с изделием"], style: "steps", take: [4, 4], pool: [
          ["Центровка и вытягивание", "Глина ставится на круг, выравнивается и поднимается в стенки. Это самая трудная часть."],
          ["Сушка и обточка", "Через сутки изделие твердеет, с него срезают лишнее и вырезают ножку."],
          ["Первый обжиг", "Девятьсот градусов, восемь часов. Глина перестаёт размокать от воды."],
          ["Глазурь и второй обжиг", "Изделие окунают в глазурь и обжигают при тысяче двухстах. После этого из него можно пить."],
        ] },
        { t: "gallery", title: ["Формы, которые делают на первом курсе", "Что получается у учеников", "Примеры работ"], take: [4, 6], pool: ["Пиала", "Кружка с ручкой", "Ваза с узким горлом", "Глубокая тарелка", "Молочник", "Маслёнка", "Чайник", "Подсвечник"] },
        { t: "prose", title: ["О мастерской", "Кто ведёт занятия", "Про нас"], take: 2, pool: [
          "В мастерской три круга и одна печь. Больше пяти человек одновременно мы не берём: иначе мастер не успевает подойти к каждому.",
          "Мы работаем с белой и красной глиной от одного поставщика. Глазури замешиваем сами, поэтому цвета немного меняются от партии к партии.",
          "Не всё получается с первого раза. Примерно каждая пятая чашка трескается при сушке, и это нормальная часть ремесла.",
          "Адрес и время мы присылаем после записи: студия занимает комнату в жилом доме, вывески нет.",
        ] },
      ],
    },
    gallery: {
      names: ["Линии и шум", "Тихий плоттер", "Сто сорок кривых", "Графика по правилам", "Точка, линия, зерно", "Медленный рисунок"],
      tags: ["Рисунки, сделанные алгоритмом и пером", "Генеративная графика на бумаге", "Серии рисунков, начерченных плоттером"],
      heroes: [
        ["Рисунки, которые чертит машина по моим правилам", "Я пишу короткую программу, она выдаёт тысячи вариантов, я выбираю несколько и отдаю их плоттеру. Перо, тушь и плотная бумага."],
        ["Каждая работа начинается с одного числа", "Число задаёт форму линий, их плотность и ритм. Измените его на единицу, и получится другой лист."],
        ["Графика, в которой рука автора заменена правилом", "Здесь собраны серии последних лет. У каждого листа есть номер и зерно, по которому его можно воспроизвести."],
      ],
      foot: ["Каждый лист существует в одном экземпляре.", "Изображения можно использовать с указанием автора.", "Код рисунков публикую по мере готовности."],
      sections: [
        { t: "gallery", title: ["Избранные листы", "Серия этого года", "Работы"], take: [6, 9], pool: ["Поле № 12", "Прилив", "Сорок три шага", "Сетка с ошибкой", "Берег", "Ветер с востока", "Перфокарта", "Тихая вода", "Рельеф", "Сгиб", "Дрейф", "Узел"] },
        { t: "prose", title: ["Как это сделано", "О методе", "Про процесс"], take: 2, pool: [
          "Плоттер ведёт перо со скоростью три сантиметра в секунду. Лист формата А3 с плотной штриховкой занимает от двух до шести часов.",
          "Я не правлю результат руками. Если лист не нравится, меняю правило или зерно и запускаю снова.",
          "Случайность здесь управляемая: одно и то же зерно всегда даёт один и тот же рисунок, до последней линии.",
          "Бумага хлопковая, триста граммов. Тушь пигментная, не выцветает и не боится воды.",
        ] },
        { t: "table", title: ["Серии", "Что есть в наличии", "Листы и размеры"], take: [3, 5], pool: [
          ["Поля", "двенадцать листов, линии разной плотности", "30 × 40 см"],
          ["Берега", "восемь листов, один цвет", "40 × 50 см"],
          ["Сетки", "двадцать листов, чёрная тушь", "21 × 30 см"],
          ["Рельефы", "шесть листов, два пера", "50 × 70 см"],
          ["Наброски", "пробные листы, без номера", "15 × 21 см"],
        ] },
        { t: "list", title: ["Заметки", "Из рабочей тетради", "Короткие записи"], style: "rows", take: [3, 4], dated: true, pool: [
          ["Новое перо", "Перешёл с линера на рапидограф 0,25. Линия стала ровнее, но перо засыхает за ночь."],
          ["Шум Перлина надоел", "Пробую строить поля из суммы синусов. Рисунок получается суше и ритмичнее."],
          ["Бумага ведёт себя", "В сырую погоду лист растягивается на миллиметр, и последняя линия не попадает в первую."],
          ["Серия закончена", "Двенадцать листов «Полей» готовы. Самый долгий чертился пять с половиной часов."],
        ] },
      ],
    },
  },
  en: {
    notes: {
      names: ["Margin Notes", "The Slow Page", "Second Cup", "North Windowsill", "Pencil and Map", "The Long Way Home", "Field Ledger"],
      tags: ["Notes on the city, books and long walks", "Written rarely and never on schedule", "One curious person's diary"],
      heroes: [
        ["Notes I write for myself, and a little for you", "There are no sections and no schedule here. When something deserves two paragraphs it ends up on this page: a walk, a finished book, a recipe that worked or a repair that did not."],
        ["Writing down what I noticed on the way", "Short pieces with no occasion. Courtyards, railway stations, old maps and how ordinary things are put together."],
        ["A blog with no news", "No roundups and no forecasts. Only what I saw myself, with a date and without a moral."],
      ],
      foot: ["Quote freely, with a link back.", "Written by hand, without hurry.", "Updated when there is something to say."],
      sections: [
        { t: "list", title: ["Latest entries", "Recently", "What is new"], style: "rows", take: [4, 6], dated: true, pool: [
          ["Tram number seven", "Rode the line from loop to loop and wrote down everything in the window: three depots, one market and a bridge where ducks spend the winter."],
          ["How I mended a stool", "Four evenings, two kinds of glue and one conclusion: a dowel holds better than a screw if you are patient."],
          ["Autumn reading list", "Seven titles, two of which I would read again. The rest get a short and honest paragraph each."],
          ["The yard with the dovecote", "Someone a block away still keeps pigeons. I asked the owner why the birds always come back."],
          ["Tea from a flask", "Why plain black tea tastes better in the cold than any rare leaf does at home."],
          ["An old map of the district", "Found a street plan forty years old and walked the streets the new map no longer has."],
          ["On handwriting", "Wrote one page a day by hand for a month. The letters got no neater, but the thoughts got shorter."],
          ["The station clocks", "Our station has three clocks, each showing a different time. I worked out which one is right."],
        ] },
        { t: "prose", title: ["About the author", "Who writes this", "A few words about me"], take: 2, pool: [
          "I work with texts and drawings, and walk in my free time. It comes to about a thousand kilometres a year, most of them in the same town.",
          "I started the blog so as not to lose the notes from my paper notebooks. Some entries are copied from there almost unchanged.",
          "There are few photographs: I shoot badly and remember well. Sometimes I draw a diagram instead.",
          "There are no comments. If you want to answer, write a letter: I read everything and reply slowly.",
        ] },
        { t: "list", title: ["Topics", "What gets written here", "Loose sections"], style: "cards", take: [3, 4], pool: [
          ["Walks", "Routes through the town and around it, with distances and places to sit down."],
          ["Reading", "Short notes on the books I finished. The unfinished ones get a mention too."],
          ["Mending", "Furniture, a bicycle, the kitchen tap. What worked, what did not and how long it took."],
          ["Kitchen", "Plain everyday food: porridge, soup and whatever gets baked at the weekend."],
          ["Maps", "Old plans, transport diagrams and sketches of my own."],
        ] },
      ],
    },
    tool: {
      names: TOOL_NAMES,
      tags: ["A small command-line utility", "One job, one binary", "A terminal tool with no dependencies"],
      heroes: [
        ["{name} finds what does not belong in large text files", "It reads a stream line by line, never loads the file into memory and writes to standard output. It works in a pipeline with other commands."],
        ["{name}: a line filter with nothing to configure", "No config files and no plugins. One file, plain flags, help that fits on one screen."],
        ["{name} makes logs readable", "It joins wrapped lines, drops repeats and aligns timestamps. Plain text in, plain text out."],
      ],
      foot: ["Source is open, MIT licence.", "Built without third-party libraries.", "Bug reports are welcome as a minimal example."],
      sections: [
        { t: "code", title: ["Install and first run", "Quick start", "How to run it"], pool: [
          ["$ curl -LO https://example.org/{name}.tar.gz", "$ tar -xzf {name}.tar.gz", "$ ./{name} --help", "", "$ cat app.log | ./{name} --since 1h --level warn"],
          ["$ make", "$ sudo make install", "", "$ {name} -n 20 access.log", "$ {name} --count '*.csv'"],
          ["$ go install example.org/{name}@latest", "", "$ {name} input.txt > clean.txt", "$ tail -f server.log | {name} --follow"],
        ] },
        { t: "list", title: ["What it does", "Features", "What it is for"], style: "cards", take: [3, 5], pool: [
          ["Streaming", "A ten-gigabyte file is read the same way as a ten-kilobyte one: memory does not grow."],
          ["Plain flags", "Every flag has a short and a long form, and the defaults are written in the help."],
          ["Exit codes", "Zero when something was found, one when nothing was, two on an error. Handy in scripts."],
          ["No colour by default", "Colour turns on only in a terminal. In a pipeline the output is always clean."],
          ["Encodings", "UTF-8 and single-byte encodings are detected, anything else is set with a flag."],
          ["One file", "A static build: copy it to a server and there is nothing else to install."],
        ] },
        { t: "table", title: ["Version history", "What changed", "Changelog"], take: [3, 5], pool: [
          ["0.9.2", "Fixed handling of empty lines at the end of a file", "March"],
          ["0.9.0", "The --follow flag for reading a growing file", "January"],
          ["0.8.1", "Help rewritten, examples moved to the top", "November"],
          ["0.8.0", "Several files in one run", "September"],
          ["0.7.3", "ARM build, the binary is a third smaller", "July"],
          ["0.7.0", "First version tested on large files", "May"],
        ] },
        { t: "prose", title: ["Why another utility", "Where it came from", "Why it is this way"], take: 2, pool: [
          "The program grew out of a twenty-line script I kept copying from server to server. At some point it was easier to write it properly.",
          "I add no new modes on purpose. If a standard command next to it does the job, it will not be here.",
          "The tests run on files taken from real logs, with addresses and names removed. The largest weighs fourteen gigabytes.",
          "Flags have stayed compatible since 0.7. Anything I remove will warn for a year first.",
        ] },
      ],
    },
    recipes: {
      names: ["The Cast Iron Pan", "Kitchen Notebook", "Salt and Flame", "Soup on Wednesdays", "The Warm Stove", "The Ladle", "A Spoon of Honey"],
      tags: ["Home recipes, tested in my own kitchen", "Plain food for every day", "A recipe notebook without extra words"],
      heroes: [
        ["Recipes that work the first time", "I cooked every dish at least three times before writing it down. Grams and minutes are exact, and nothing calls for a rare ingredient."],
        ["Cooking at home: soup, bread and something for tea", "This is what gets eaten on ordinary days. No restaurant plating, but a shopping list you can follow."],
        ["A kitchen notebook moved online", "The recipes are written the way I dictate them to friends over the phone: what to buy, what to do and when to take it off the heat."],
      ],
      foot: ["Weights are given before peeling.", "Times are for an ordinary electric hob.", "Copy the recipes and change them to your taste."],
      sections: [
        { t: "list", title: ["What to cook this week", "New recipes", "Written down lately"], style: "cards", take: [4, 6], pool: [
          ["Fresh cabbage soup", "Beef stock, cabbage, potatoes and a spoon of tomato at the very end. Rests for an hour under the lid.", "1 h 40 min"],
          ["Buckwheat with mushrooms", "The grain is toasted in a dry pan, the mushrooms cooked apart. They meet five minutes before serving.", "35 min"],
          ["Sourdough wheat loaf", "Mixed in the evening, proved overnight in the fridge, baked in the morning in a cast iron pot.", "18 h"],
          ["Curd pancakes without flour", "Dry curd cheese, an egg yolk and semolina. Shaped with wet hands, fried over medium heat.", "25 min"],
          ["Tomato soup with beans", "Tinned tomatoes, white beans, garlic and stale bread. Ready before the kettle boils twice.", "30 min"],
          ["Baked apples with walnuts", "The core comes out, walnuts and honey go in. Forty minutes at a hundred and eighty.", "50 min"],
          ["Potato pancakes", "Finely grated potato, onion, an egg. Squeeze out the juice or the pancakes turn grey.", "40 min"],
          ["Oat biscuits with raisins", "Rolled oats, butter, sugar, an egg. The tray comes out while the middle is still soft.", "30 min"],
        ] },
        { t: "table", title: ["Beetroot soup for a large pot: what to buy", "Shopping for Sunday soup", "The market list"], take: [5, 7], pool: [
          ["Beef on the bone", "shoulder or brisket", "700 g"],
          ["Beetroot", "two medium", "400 g"],
          ["White cabbage", "a quarter of a head", "350 g"],
          ["Potatoes", "three", "300 g"],
          ["Carrot and onion", "one of each", "250 g"],
          ["Tomato paste", "a heaped tablespoon", "40 g"],
          ["Garlic", "three cloves", "15 g"],
          ["Cider vinegar", "so the beetroot keeps its colour", "1 tbsp"],
        ] },
        { t: "list", title: ["How to read these recipes", "A few kitchen rules", "Before you start"], style: "steps", take: [3, 4], pool: [
          ["Read to the end first", "The middle of a recipe should never surprise you with “soak overnight”."],
          ["Weigh and set out", "With everything cut and standing by the stove, cooking is twice as calm."],
          ["Salt in two goes", "Half at the start, half at the end, after tasting. Too much is harder to mend than too little."],
          ["Let it rest", "Soup, meat and bakes keep cooking for ten minutes off the heat. Do not cut at once."],
        ] },
        { t: "prose", title: ["About the notebook", "Where the recipes come from", "Who cooks"], take: 2, pool: [
          "My grandmother gave me the first notebook: forty recipes and not one exact weight. I cooked from it and wrote the grams in.",
          "I am not a chef. I cook every day for a family of four and write down what they ask for again.",
          "Season matters more than novelty here: soup and baking in winter, vegetables and nothing that needs an oven in summer.",
          "If a dish did not work, it is most likely the stove. Try less heat and more time.",
        ] },
      ],
    },
    club: {
      names: ["The Yellow Meeple Club", "Board Game Thursdays", "The Six-Sider Club", "Riverside Game Night", "The Long Game Club", "Knight's Move Club"],
      tags: ["Board games in the evening, free to join", "We play every week and teach newcomers at the table", "A club for people who like board games"],
      heroes: [
        ["Come and play: we explain the rules and find you a table", "We meet once a week and set out six to ten games. You need not bring your own, and you need no experience."],
        ["An evening of board games, no tournaments and no fees", "An ordinary crowd: some have played for twenty years, some came for the first time. Every table has someone who knows the rules."],
        ["Sit down at any table", "A game takes from twenty minutes to three hours. Tell us at the door how long you have, and we will pick one."],
      ],
      foot: ["Games from the shared shelf go back in the box complete.", "No food at the tables with cards.", "The club runs on members' voluntary contributions."],
      sections: [
        { t: "table", title: ["This month's schedule", "When we play", "Next meetings"], take: [4, 5], pool: [
          ["First Thursday", "family games, under an hour each", "19:00"],
          ["Second Thursday", "strategy games for the whole evening", "18:30"],
          ["Third Thursday", "card games and duels", "19:00"],
          ["Fourth Thursday", "new on the shelf, rules taught", "19:00"],
          ["Last Saturday", "long games with lunch", "12:00"],
          ["Every other Sunday", "games with children from eight", "11:00"],
        ] },
        { t: "list", title: ["What is on the shelf", "What we play", "The club's games"], style: "cards", take: [4, 6], pool: [
          ["Catan", "Trading resources and building roads on an island. Four players, an hour and a half.", "easy to start"],
          ["Carcassonne", "A map built of square tiles, with cities to claim. Explained in five minutes.", "easy to start"],
          ["Ticket to Ride", "Building railway routes between cities. Good for a first visit.", "easy to start"],
          ["Agricola", "A small farm where food is always short. A lot to think about.", "harder"],
          ["Codenames", "Two teams and one clue word for several cards. Up to twelve people.", "for a crowd"],
          ["Chess and Go", "Two boards stand by the window at all times. Clocks are there, blitz if you like.", "for two"],
          ["Dixit", "Pictures and one-line hints. Everyone laughs, nobody counts too hard.", "for a crowd"],
        ] },
        { t: "list", title: ["Coming for the first time", "Your first evening", "If you have not been here"], style: "steps", take: [3, 3], pool: [
          ["Come at the start", "Tables form in the first half hour. Later works too, but you wait for a game to end."],
          ["Say what you have played", "Even if it was only bingo. It decides which game you are offered."],
          ["Play one game, then decide", "No forms and no membership. If you liked it, come back next week."],
        ] },
        { t: "prose", title: ["Club rules", "About us", "How it works"], take: 2, pool: [
          "The club began with four friends and one box. The shelf now holds more than sixty games, some of them given by members.",
          "We sell no games and run no paid events. Tea and biscuits are bought by everyone chipping in.",
          "There is one main rule: lose calmly. Nobody at the table gives advice unless asked.",
          "A game can go home with you for a week, once your name is in the notebook by the shelf.",
        ] },
      ],
    },
    studio: {
      names: ["Fired Clay Workshop", "The Upstairs Pottery", "Wheel and Kiln", "Shard Ceramics Studio", "Grey Clay Workshop"],
      tags: ["A ceramics workshop: classes and open studio", "Potter's wheels, glazes and an eighty-litre kiln", "A small ceramics studio"],
      heroes: [
        ["Throw a cup with your own hands and collect it in two weeks", "In the first class you sit at the wheel and make a simple form. We dry it, fire it and glaze it in the colour you choose."],
        ["The potter's wheel for people who have never tried", "Groups of up to five, a wheel for each. Clay, tools and an apron are already here."],
        ["A workshop where you work with clay at your own pace", "Classes with a teacher for beginners, and open hours for those who can already centre."],
      ],
      foot: ["Firing and glaze are included in the class price.", "Finished pieces are kept for a month.", "We use clay and glazes free of lead."],
      sections: [
        { t: "table", title: ["Classes and prices", "What you can choose", "Formats"], take: [3, 5], pool: [
          ["First class at the wheel", "2 hours, one piece", "€35"],
          ["Course of four classes", "wheel, hand building, glazing", "€120"],
          ["Open studio", "an hour of work without a teacher", "€9"],
          ["Class for two", "two wheels side by side, 2 hours", "€60"],
          ["Clay with children", "from six years old, 1.5 hours", "€25"],
          ["Firing your own work", "per litre of kiln space", "€2"],
        ] },
        { t: "list", title: ["How the work goes", "From a lump to a cup", "What happens to a piece"], style: "steps", take: [4, 4], pool: [
          ["Centring and pulling", "The clay goes on the wheel, is trued and raised into walls. This is the hardest part."],
          ["Drying and trimming", "A day later the piece is firm, the excess is cut away and the foot is carved."],
          ["First firing", "Nine hundred degrees, eight hours. The clay no longer dissolves in water."],
          ["Glaze and second firing", "The piece is dipped in glaze and fired at twelve hundred. After that you can drink from it."],
        ] },
        { t: "gallery", title: ["Forms made in the first course", "What students make", "Examples"], take: [4, 6], pool: ["Bowl", "Mug with a handle", "Narrow-necked vase", "Deep plate", "Milk jug", "Butter dish", "Teapot", "Candle holder"] },
        { t: "prose", title: ["About the workshop", "Who teaches", "About us"], take: 2, pool: [
          "The workshop has three wheels and one kiln. We take no more than five people at a time, or the teacher cannot reach everyone.",
          "We work with white and red clay from one supplier. The glazes are mixed here, so colours shift a little from batch to batch.",
          "Not everything works the first time. About one cup in five cracks while drying, and that is an ordinary part of the craft.",
          "We send the address and time after you book: the studio is a room in a residential building, with no sign outside.",
        ] },
      ],
    },
    gallery: {
      names: ["Lines and Noise", "The Quiet Plotter", "One Hundred Forty Curves", "Drawing by Rule", "Dot, Line, Seed", "Slow Drawing"],
      tags: ["Drawings made by an algorithm and a pen", "Generative drawings on paper", "Series of drawings made on a pen plotter"],
      heroes: [
        ["Drawings a machine makes by my rules", "I write a short program, it gives thousands of variants, I choose a few and hand them to the plotter. Pen, ink and heavy paper."],
        ["Every work starts with one number", "The number sets the shape of the lines, their density and rhythm. Change it by one and you get another sheet."],
        ["Drawings where a rule stands in for the hand", "These are the series of recent years. Each sheet has a number and a seed it can be made again from."],
      ],
      foot: ["Each sheet exists in one copy.", "Images may be used with credit.", "I publish the code of the drawings as it gets ready."],
      sections: [
        { t: "gallery", title: ["Selected sheets", "This year's series", "Works"], take: [6, 9], pool: ["Field No. 12", "Rising Tide", "Forty-Three Steps", "Grid with an Error", "Shore", "East Wind", "Punch Card", "Still Water", "Relief", "Fold", "Drift", "Knot"] },
        { t: "prose", title: ["How it is made", "On the method", "About the process"], take: 2, pool: [
          "The plotter moves the pen at three centimetres a second. An A3 sheet with dense hatching takes two to six hours.",
          "I do not correct the result by hand. If I dislike a sheet, I change the rule or the seed and run it again.",
          "The randomness is controlled: the same seed always gives the same drawing, to the last line.",
          "The paper is cotton, three hundred grams. The ink is pigment: it does not fade and does not mind water.",
        ] },
        { t: "table", title: ["Series", "What is available", "Sheets and sizes"], take: [3, 5], pool: [
          ["Fields", "twelve sheets, lines of varying density", "30 × 40 cm"],
          ["Shores", "eight sheets, one colour", "40 × 50 cm"],
          ["Grids", "twenty sheets, black ink", "21 × 30 cm"],
          ["Reliefs", "six sheets, two pens", "50 × 70 cm"],
          ["Sketches", "trial sheets, unnumbered", "15 × 21 cm"],
        ] },
        { t: "list", title: ["Notes", "From the workbook", "Short entries"], style: "rows", take: [3, 4], dated: true, pool: [
          ["A new pen", "Moved from a fineliner to a 0.25 technical pen. The line is steadier, but the pen dries out overnight."],
          ["Tired of Perlin noise", "Trying fields built from a sum of sines. The drawing comes out drier and more rhythmic."],
          ["Paper has a mind", "In damp weather a sheet stretches by a millimetre, and the last line misses the first."],
          ["A series is done", "The twelve sheets of “Fields” are ready. The longest took five and a half hours to draw."],
        ] },
      ],
    },
  },
};
