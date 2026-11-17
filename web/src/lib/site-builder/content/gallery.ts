import type { KindContent } from "./types";

/** The words of a site of the kind "gallery": both languages, and the pages it is made of. */
export const gallery: KindContent = {
  pages: [
    { path: "", title: { ru: "Главная", en: "Home" }, sections: [0] },
    { path: "series", title: { ru: "Серии", en: "Series" }, sections: [2] },
    { path: "about", title: { ru: "О методе", en: "About" }, sections: [1, 3] },
  ],
  ru: {
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
  en: {
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
};
