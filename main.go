package main

import (
	"context"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	uuid "github.com/satori/go.uuid"
	"gorm.io/gorm"
	"log"
	"panionbot/commandModule"
	"panionbot/helpFunc"
	"panionbot/keyboard"
	"panionbot/models"
	"strconv"
	"strings"
	"sync"
	"time"
)

// var workerPool = make(chan struct{}, 250000)
const maxConcurrency = 100

func main() {

	luceneHost := helpFunc.GetTextFromFile("./token/lucene.txt")
	db, err := helpFunc.SetupDatabase()
	botToken := helpFunc.GetTextFromFile("./token/botToken.txt")
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	//bot.Debug = true

	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	updatesChan := make(chan tgbotapi.Update, maxConcurrency)

	// Запуск горутин для обработки обновлений
	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)
		go updateWorker(ctx, bot, db, luceneHost, updatesChan, &wg)
	}

	for update := range updates {
		select {
		case <-ctx.Done():
			break
		case updatesChan <- update:
		}
	}
	wg.Wait()
}

func updateWorker(ctx context.Context, bot *tgbotapi.BotAPI, db *gorm.DB, luceneHost string, updatesChan <-chan tgbotapi.Update, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updatesChan:
			if !ok {
				return
			}

			processUpdate(bot, db, update, luceneHost)
		}
	}
}

func processUpdate(bot *tgbotapi.BotAPI, db *gorm.DB, update tgbotapi.Update, luceneHost string) {
	defer func() {
		if r := recover(); r != nil {
			errorMessage := "Извините, произошла внутренняя ошибка. Мы работаем над ее решением."
			adminID, _ := strconv.ParseInt((helpFunc.GetTextFromFile("./token/adminID.txt")), 10, 64)
			helpFunc.SendMessage(bot, update.Message.Chat.ID, errorMessage)
			helpFunc.SendMessage(bot, adminID, errorMessage)
			log.Println("Recovered from panic:", r)
		}
	}()

	switch {
	case update.InlineQuery != nil:
		handleInlineQuery(bot, update.InlineQuery, luceneHost)
	case update.Message != nil:
		handleMessage(bot, db, update.Message, luceneHost)
	case update.CallbackQuery != nil:
		handleCallbackQuery(bot, update.CallbackQuery)

	}
}

type OneAnek struct {
	Text string `json:"text"`
}

func handleInlineQuery(bot *tgbotapi.BotAPI, inlineQuery *tgbotapi.InlineQuery, luceneHost string) {
	anekdoty := commandModule.FindAnek(inlineQuery.Query, luceneHost)

	var articles []tgbotapi.InlineQueryResultArticle
	var articleGroup sync.WaitGroup
	var mu sync.Mutex // Mutex для синхронизации доступа к разделяемым данным

	// Определение максимального числа одновременно работающих горутин
	maxConcurrency := 25
	semaphore := make(chan struct{}, maxConcurrency)

	if len(anekdoty.Items) != 0 {
		for _, anek := range anekdoty.Items {
			articleGroup.Add(1)
			semaphore <- struct{}{} // Захватываем слот семафора

			go func(anek OneAnek) {
				defer func() {
					<-semaphore // Освобождаем слот семафора
					articleGroup.Done()
				}()

				article := tgbotapi.NewInlineQueryResultArticle(uuid.NewV4().String(), " ", anek.Text)
				article.Description = anek.Text

				mu.Lock()
				articles = append(articles, article)
				mu.Unlock()
			}(anek)
		}
	}

	articleGroup.Wait()

	if len(anekdoty.Items) == 50 {
		articles[0].Title = "Результатов: >50. Отображено: 50. Уточните запрос"
	} else if len(anekdoty.Items) != 0 {
		articles[0].Title = "Результатов: " + strconv.Itoa(len(anekdoty.Items))
	} else {
		articleGroup.Add(1)
		s := "Empty :( (анекдотов не найдено)"
		article := tgbotapi.NewInlineQueryResultArticle(s, s, s)
		mu.Lock()
		articles = append(articles, article)
		mu.Unlock()
		articleGroup.Done()
	}

	b := make([]interface{}, len(articles))
	for i := range articles {
		b[i] = articles[i]
	}

	inlineConf := tgbotapi.InlineConfig{
		InlineQueryID: inlineQuery.ID,
		IsPersonal:    true,
		CacheTime:     0,
		Results:       b,
	}

	if _, err := bot.Request(inlineConf); err != nil {
		log.Println("Error sending inline query results:", err)
	}
}

func handleMessage(bot *tgbotapi.BotAPI, db *gorm.DB, message *tgbotapi.Message, luceneHost string) {
	// Extracting relevant information from the update
	user := models.Users{}
	group := models.Groups{}
	//userGroup := models.UsersGroups{}

	userID := message.From.ID
	userName := message.From.UserName
	groupName := message.Chat.Title

	chatID := message.Chat.ID
	user.UserID = userID
	user.UserName = userName
	group.GroupName = groupName
	group.GroupID = chatID
	msg := tgbotapi.NewMessage(message.Chat.ID, message.Text)

	if db.First(&user, "user_id = ?", userID).RowsAffected > 0 {
		if user.UserName != userName {
			db.Model(&user).Update("user_name", userName)
		}
	}

	if message.IsCommand() {
		switch message.Command() {
		case "start":
			msg.Text = "Я пока ещё жив"
		case "anek":
			msg.Text = commandModule.FindRandomAnek(0, luceneHost)
		case "anek_1":
			msg.Text = commandModule.FindRandomAnek(1, luceneHost)
		case "anek_2":
			msg.Text = commandModule.FindRandomAnek(2, luceneHost)
		case "horoscope":
			msg.ReplyMarkup = keyboard.Horoscope

		case "weather_report":

			if message.Chat.Type == "private" {
				msg.ReplyMarkup = keyboard.Weather
				msg.Text = "Взгляните на клавиатуру"

			} else {
				msg.Text = "Данная команда не работает в группах"
			}
		case "reg":
			if helpFunc.IsGroupChat(message.Chat.Type) {
				result := helpFunc.HandleCommandReg(db, user, userID, chatID, groupName)

				msg.Text = result

			} else {
				msg.Text = "Данная команда работает только в группах"
			}
		case "bunny_tomato":
			if helpFunc.IsGroupChat(message.Chat.Type) {
				result := helpFunc.HandleCommandBunnyTomato(bot, db, group, chatID, groupName)

				msg.Text = result

			} else {
				msg.Text = "Данная команда работает только в группах"
			}
		case "group_stat":
			if helpFunc.IsGroupChat(message.Chat.Type) {
				result := helpFunc.HandleCommandGroupStat(db, chatID)

				msg.Text = result

			} else {
				msg.Text = "Данная команда работает только в группах"
			}
		case "my_stat":
			if helpFunc.IsGroupChat(message.Chat.Type) {
				result := helpFunc.HandleCommandMyStat(db, int(userID), chatID)

				msg.Text = result

			} else {
				msg.Text = "Данная команда работает только в группах"
			}
		case "bot_time":
			msg.Text = time.Now().String()
		default:
			imgPath := "./token/What.png"
			helpFunc.SendImage(bot, chatID, imgPath, "Wait")
			msg.Text = "What?"
		}

		defer func() {
			if r := recover(); r != nil {
				errorMessage := "Извините, произошла внутренняя ошибка. Мы работаем над ее решением."
				adminID, _ := strconv.ParseInt((helpFunc.GetTextFromFile("./token/adminID.txt")), 10, 64)
				helpFunc.SendMessage(bot, message.Chat.ID, errorMessage)

				helpFunc.SendMessage(bot, adminID, errorMessage)
				log.Println("Recovered from panic:", r)
			}
		}()

		if _, err := bot.Send(msg); err != nil {
			log.Panic(err)
		}

	}
	if message.Text == "По названию" {
		msg.Text = "Напишите город в котором хотите узнать погоду"
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true}
		if _, err := bot.Send(msg); err != nil {
			log.Println("Error City Name: ", err)
		}
	}

	if message.ReplyToMessage != nil && message.Chat.Type == "private" {
		msg.Text = commandModule.GetWeatherByName(message.Text)

		if message.Location != nil {
			msg.Text = commandModule.GetWeatherByLocation(message.Location.Latitude, message.Location.Longitude)
		}
		if _, err := bot.Send(msg); err != nil {
			log.Println("Error Reply: ", err)
		}
	}
}

func handleCallbackQuery(bot *tgbotapi.BotAPI, callbackQuery *tgbotapi.CallbackQuery) {
	// Проверяем, что callbackQuery не nil
	if callbackQuery == nil {
		log.Println("Received nil callbackQuery")
		return
	}

	// Отправляем подтверждение о получении колбэка
	callback := tgbotapi.NewCallback(callbackQuery.ID, callbackQuery.Data)
	if _, err := bot.Request(callback); err != nil {
		log.Println("Error sending callback confirmation:", err)
		return
	}

	// Получаем текст гороскопа
	horoscopeText := strings.ToUpper(callbackQuery.Data) + ": " + commandModule.GetHoroscope(callbackQuery.Data)
	msg := tgbotapi.NewMessage(callbackQuery.Message.Chat.ID, horoscopeText)

	// Отправляем новое сообщение
	if _, err := bot.Send(msg); err != nil {
		log.Println("Error sending horoscope message:", err)
	}

	// Удаляем старое сообщение с инлайн-кнопками
	deleteMsg := tgbotapi.NewDeleteMessage(callbackQuery.Message.Chat.ID, callbackQuery.Message.MessageID)
	if _, err := bot.Request(deleteMsg); err != nil {
		log.Println("Error deleting inline keyboard message:", err)
	}
}
