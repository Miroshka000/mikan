import type { KindContent } from "./types";

/** The words of a site of the kind "recipes": both languages, and the pages it is made of. */
export const recipes: KindContent = {
  pages: [
    { path: "", title: { ru: "Главная", en: "Home" }, sections: [0] },
    { path: "shopping", title: { ru: "Список покупок", en: "Shopping list" }, sections: [1] },
    { path: "about", title: { ru: "О тетради", en: "About" }, sections: [3, 2] },
  ],
  ru: {
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
  en: {
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
};
