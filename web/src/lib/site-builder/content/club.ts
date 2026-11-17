import type { KindContent } from "./types";

/** The words of a site of the kind "club": both languages, and the pages it is made of. */
export const club: KindContent = {
  pages: [
    { path: "", title: { ru: "Главная", en: "Home" }, sections: [0] },
    { path: "games", title: { ru: "Игры", en: "Games" }, sections: [1] },
    { path: "join", title: { ru: "Как прийти", en: "Join us" }, sections: [2, 3] },
  ],
  ru: {
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
  en: {
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
};
