import type { KindContent } from "./types";

/** A tool's name is two short words put together. */
const TOOL_NAMES: [string[], string[]] = [
  ["path", "log", "tab", "glob", "line", "diff", "cron", "env", "csv", "note"],
  ["sift", "trim", "pick", "fold", "peek", "sort", "mend", "tidy"],
];

/** The words of a site of the kind "tool": both languages, and the pages it is made of. */
export const tool: KindContent = {
  pages: [
    { path: "", title: { ru: "Главная", en: "Home" }, sections: [0, 1] },
    { path: "changelog", title: { ru: "Версии", en: "Changelog" }, sections: [2] },
    { path: "about", title: { ru: "О проекте", en: "About" }, sections: [3] },
  ],
  ru: {
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
  en: {
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
};
