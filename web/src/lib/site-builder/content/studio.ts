import type { KindContent } from "./types";

/** The words of a site of the kind "studio": both languages, and the pages it is made of. */
export const studio: KindContent = {
  pages: [
    { path: "", title: { ru: "Главная", en: "Home" }, sections: [0] },
    { path: "process", title: { ru: "Как проходит работа", en: "Process" }, sections: [1, 2] },
    { path: "about", title: { ru: "О мастерской", en: "About" }, sections: [3] },
  ],
  ru: {
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
  en: {
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
};
