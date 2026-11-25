import type { KindContent } from "./types";

/** The words of a site of the kind "notes": both languages, and the pages it is made of. */
export const notes: KindContent = {
  pages: [
    { path: "", title: { ru: "Главная", en: "Home" }, sections: [0, 2] },
    { path: "archive", title: { ru: "Архив", en: "Archive" }, sections: [0] },
    { path: "about", title: { ru: "Об авторе", en: "About" }, sections: [1] },
  ],
  ru: {
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
  en: {
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
};
