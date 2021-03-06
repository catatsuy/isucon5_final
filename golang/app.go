package main

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	goCache "github.com/pmylund/go-cache"

	"github.com/gorilla/context"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	_ "github.com/lib/pq"
	"github.com/walf443/stopwatch"
)

var (
	db         *sql.DB
	store      *sessions.CookieStore
	exCache    = goCache.New(10*time.Second, 5*time.Second)
	tenkiCache = goCache.New(1*time.Second, 1*time.Second)
	userCache  = goCache.New(120*time.Second, 30*time.Second)
)

var kenCache map[string]Data
var ken2Cache map[string]Data
var surnameCache map[string]Data
var givennameCache map[string]Data

type User struct {
	ID    int
	Email string
	Grade string
}

type Arg map[string]*Service

type Service struct {
	Token  string            `json:"token"`
	Keys   []string          `json:"keys"`
	Params map[string]string `json:"params"`
}

type Data struct {
	Service string                 `json:"service"`
	Data    map[string]interface{} `json:"data"`
}

var saltChars = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, "isucon5q-go.session")
	return session
}

func getTemplatePath(file string) string {
	return path.Join("templates", file)
}

func render(w http.ResponseWriter, r *http.Request, status int, file string, data interface{}) {
	tpl := template.Must(template.New(file).ParseFiles(getTemplatePath(file)))
	w.WriteHeader(status)
	checkErr(tpl.Execute(w, data))
}

func authenticate(w http.ResponseWriter, r *http.Request, email, passwd string) *User {
	query := `SELECT id, email, grade FROM users WHERE email=$1 AND passhash=digest(salt || $2, 'sha512')`
	row := db.QueryRow(query, email, passwd)
	user := User{}
	err := row.Scan(&user.ID, &user.Email, &user.Grade)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		checkErr(err)
	}
	session := getSession(w, r)
	session.Values["user_id"] = user.ID
	session.Save(r, w)
	return &user
}

func getCurrentUser(w http.ResponseWriter, r *http.Request) *User {
	u := context.Get(r, "user")
	if u != nil {
		user := u.(User)
		return &user
	}
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if !ok || userID == nil {
		return nil
	}

	user := User{}

	key := fmt.Sprintf("user_%d", userID)
	cache, found := userCache.Get(key)
	if found {
		user = cache.(User)
	} else {
		row := db.QueryRow(`SELECT id,email,grade FROM users WHERE id=$1`, userID)
		err := row.Scan(&user.ID, &user.Email, &user.Grade)
		if err == sql.ErrNoRows {
			clearSession(w, r)
			return nil
		}
		checkErr(err)
		userCache.Set(key, user, 120*time.Second)
	}

	//row := db.QueryRow(`SELECT id,email,grade FROM users WHERE id=$1`, userID)
	//user := User{}
	//err := row.Scan(&user.ID, &user.Email, &user.Grade)
	//if err == sql.ErrNoRows {
	//clearSession(w, r)
	//return nil
	//}
	context.Set(r, "user", user)
	return &user
}

func generateSalt() string {
	salt := make([]rune, 32)
	for i := range salt {
		salt[i] = saltChars[rand.Intn(len(saltChars))]
	}
	return string(salt)
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	delete(session.Values, "user_id")
	session.Save(r, w)
}

func GetSignUp(w http.ResponseWriter, r *http.Request) {
	clearSession(w, r)
	render(w, r, http.StatusOK, "signup.html", nil)
}

func PostSignUp(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	passwd := r.FormValue("password")
	grade := r.FormValue("grade")
	salt := generateSalt()
	insertUserQuery := `INSERT INTO users (email,salt,passhash,grade) VALUES ($1,$2,digest($3 || $4, 'sha512'),$5) RETURNING id`
	insertSubscriptionQuery := `INSERT INTO subscriptions (user_id,arg) VALUES ($1,$2)`
	tx, err := db.Begin()
	checkErr(err)
	row := tx.QueryRow(insertUserQuery, email, salt, salt, passwd, grade)

	var userId int
	err = row.Scan(&userId)
	if err != nil {
		tx.Rollback()
		checkErr(err)
	}
	_, err = tx.Exec(insertSubscriptionQuery, userId, "{}")
	if err != nil {
		tx.Rollback()
		checkErr(err)
	}
	checkErr(tx.Commit())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func PostCancel(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/signup", http.StatusSeeOther)
}

func GetLogin(w http.ResponseWriter, r *http.Request) {
	clearSession(w, r)
	render(w, r, http.StatusOK, "login.html", nil)
}

func PostLogin(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	passwd := r.FormValue("password")
	authenticate(w, r, email, passwd)
	if getCurrentUser(w, r) == nil {
		http.Error(w, "Failed to login.", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func GetLogout(w http.ResponseWriter, r *http.Request) {
	clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func GetIndex(w http.ResponseWriter, r *http.Request) {
	if getCurrentUser(w, r) == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	render(w, r, http.StatusOK, "main.html", struct{ User User }{*getCurrentUser(w, r)})
}

func GetUserJs(w http.ResponseWriter, r *http.Request) {
	if getCurrentUser(w, r) == nil {
		http.Error(w, "Failed to login.", http.StatusForbidden)
		return
	}
	render(w, r, http.StatusOK, "user.js", struct{ Grade string }{getCurrentUser(w, r).Grade})
}

func GetModify(w http.ResponseWriter, r *http.Request) {
	user := getCurrentUser(w, r)
	if user == nil {
		http.Error(w, "Failed to login.", http.StatusForbidden)
		return
	}
	row := db.QueryRow(`SELECT arg FROM subscriptions WHERE user_id=$1`, user.ID)
	var arg string
	err := row.Scan(&arg)
	if err == sql.ErrNoRows {
		arg = "{}"
	}
	render(w, r, http.StatusOK, "modify.html", struct {
		User User
		Arg  string
	}{*user, arg})
}

func PostModify(w http.ResponseWriter, r *http.Request) {
	user := getCurrentUser(w, r)
	if user == nil {
		http.Error(w, "Failed to login.", http.StatusForbidden)
		return
	}

	service := r.FormValue("service")
	token := r.FormValue("token")
	keysStr := r.FormValue("keys")
	keys := []string{}
	if keysStr != "" {
		keys = regexp.MustCompile("\\s+").Split(keysStr, -1)
	}
	paramName := r.FormValue("param_name")
	paramValue := r.FormValue("param_value")

	selectQuery := `SELECT arg FROM subscriptions WHERE user_id=$1 FOR UPDATE`
	updateQuery := `UPDATE subscriptions SET arg=$1 WHERE user_id=$2`

	tx, err := db.Begin()
	checkErr(err)
	row := tx.QueryRow(selectQuery, user.ID)
	var jsonStr string
	err = row.Scan(&jsonStr)
	if err == sql.ErrNoRows {
		jsonStr = "{}"
	} else if err != nil {
		tx.Rollback()
		checkErr(err)
	}
	var arg Arg
	err = json.Unmarshal([]byte(jsonStr), &arg)
	if err != nil {
		tx.Rollback()
		checkErr(err)
	}

	if _, ok := arg[service]; !ok {
		arg[service] = &Service{}
	}
	if token != "" {
		arg[service].Token = token
	}
	if len(keys) > 0 {
		arg[service].Keys = keys
	}
	if arg[service].Params == nil {
		arg[service].Params = make(map[string]string)
	}
	if paramName != "" && paramValue != "" {
		arg[service].Params[paramName] = paramValue
	}

	b, err := json.Marshal(arg)
	if err != nil {
		tx.Rollback()
		checkErr(err)
	}
	_, err = tx.Exec(updateQuery, string(b), user.ID)
	checkErr(err)

	tx.Commit()

	http.Redirect(w, r, "/modify", http.StatusSeeOther)
}

var (
	sslClient = &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 12,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	}}
	myClient = &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 12,
	}}
)

func fetchApi(method, uri string, headers, params map[string]string) map[string]interface{} {
	var client *http.Client

	if strings.HasPrefix(uri, "https://") {
		client = sslClient
	} else {
		client = myClient
	}
	values := url.Values{}
	for k, v := range params {
		values.Add(k, v)
	}

	var req *http.Request
	var err error
	switch method {
	case "GET":
		req, err = http.NewRequest(method, uri, nil)
		checkErr(err)
		req.URL.RawQuery = values.Encode()
		break
	case "POST":
		req, err = http.NewRequest(method, uri, strings.NewReader(values.Encode()))
		checkErr(err)
		break
	}

	for k, v := range headers {
		req.Header.Add(k, v)
	}
	resp, err := client.Do(req)
	checkErr(err)

	defer resp.Body.Close()

	var data map[string]interface{}
	d := json.NewDecoder(resp.Body)
	d.UseNumber()
	checkErr(d.Decode(&data))
	return data
}

func logcache(t0 time.Time) func(hit string, userID int, service string, key string) {
	return func(hit string, userID int, service string, key string) {
		log.Printf("cache:%s\tuser_id:%d\tservice:%s\tkey:%s\ttime:%d", hit, userID, service, key, time.Now().Sub(t0).Nanoseconds())
	}
}

func GetData(w http.ResponseWriter, r *http.Request) {
	stopwatch.Watch("GetData")
	user := getCurrentUser(w, r)
	if user == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	row := db.QueryRow(`SELECT arg FROM subscriptions WHERE user_id=$1`, user.ID)
	var argJson string
	checkErr(row.Scan(&argJson))
	var arg Arg
	checkErr(json.Unmarshal([]byte(argJson), &arg))

	data := make([]Data, 0, len(arg))
	var services []string
	for service, _ := range arg {
		services = append(services, `'`+service+`'`)
	}
	if len(services) == 0 {
		w.Header().Set("Content-Type", "application/json")
		body, err := json.Marshal(data)
		checkErr(err)
		w.Write(body)
		return
	}
	query := fmt.Sprintf(`SELECT meth, token_type, token_key, uri, service FROM endpoints WHERE service IN (%s)`, strings.Join(services, ","))

	rows, err := db.Query(query)
	defer rows.Close()
	checkErr(err)

	for rows.Next() {
		var method string
		var tokenType *string
		var tokenKey *string
		var uriTemplate *string
		var serv *string
		checkErr(rows.Scan(&method, &tokenType, &tokenKey, &uriTemplate, &serv))
		service := *serv

		conf, _ := arg[service]

		headers := make(map[string]string)
		params := conf.Params
		if params == nil {
			params = make(map[string]string)
		}

		if tokenType != nil && tokenKey != nil {
			switch *tokenType {
			case "header":
				headers[*tokenKey] = conf.Token
				break
			case "param":
				params[*tokenKey] = conf.Token
				break
			}
		}

		ks := make([]interface{}, len(conf.Keys))
		ks2 := make([]string, len(conf.Keys))
		for i, s := range conf.Keys {
			ks[i] = s
			ks2[i] = s
		}
		uri := fmt.Sprintf(*uriTemplate, ks...)

		stopwatch.Watch(service + " start")
		lc := logcache(time.Now())

		if service == "ken" {
			key := ks2[0]
			cache, ok := kenCache[key]
			if ok {
				data = append(data, cache)
				lc("hit", user.ID, service, key)
			} else {
				d := Data{service, fetchApi(method, uri, headers, params)}
				kenCache[key] = d
				data = append(data, d)
				lc("miss", user.ID, service, key)
			}
		} else if service == "ken2" {
			q, _ := params["zipcode"]
			cache, ok := ken2Cache[q]
			if ok {
				data = append(data, cache)
				lc("hit", user.ID, service, q)
			} else {
				d := Data{service, fetchApi(method, uri, headers, params)}
				ken2Cache[q] = d
				data = append(data, d)
				lc("miss", user.ID, service, q)
			}
		} else if service == "surname" {
			q, _ := params["q"]
			cache, ok := surnameCache[q]
			if ok {
				data = append(data, cache)
				lc("hit", user.ID, service, q)
			} else {
				d := Data{service, fetchApi(method, uri, headers, params)}
				surnameCache[q] = d
				data = append(data, d)
				lc("miss", user.ID, service, q)
			}
		} else if service == "givenname" {
			q, _ := params["q"]
			cache, ok := givennameCache[q]
			if ok {
				data = append(data, cache)
				lc("hit", user.ID, service, q)
			} else {
				d := Data{service, fetchApi(method, uri, headers, params)}
				givennameCache[q] = d
				data = append(data, d)
				lc("miss", user.ID, service, q)
			}
		} else if service == "tenki" {
			q, _ := params["zipcode"]
			cache, found := tenkiCache.Get(q)
			if found {
				data = append(data, cache.(Data))
				lc("hit", user.ID, service, q)
			} else {
				d := Data{service, fetchApi(method, uri, headers, params)}
				tenkiCache.Set(q, d, 2*time.Second)
				data = append(data, d)
				lc("miss", user.ID, service, q)
			}
		} else if service == "perfectsec_attacked" {
			key := fmt.Sprintf("pa_%s", headers["X-PERFECT-SECURITY-TOKEN"])
			cache, found := exCache.Get(key)
			if found {
				data = append(data, cache.(Data))
				lc("hit", user.ID, service, key)
			} else {
				d := Data{service, fetchApi(method, uri, headers, params)}
				exCache.Set(key, d, 10*time.Second)
				data = append(data, d)
				lc("miss", user.ID, service, key)
			}
		} else {
			data = append(data, Data{service, fetchApi(method, uri, headers, params)})
			lc("uncached", user.ID, service, "-")
		}
		stopwatch.Watch(service + " finish")
	}

	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(data)
	checkErr(err)
	w.Write(body)
}

func GetInitialize(w http.ResponseWriter, r *http.Request) {
	fname := "../sql/initialize.sql"
	file, err := filepath.Abs(fname)
	checkErr(err)
	_, err = exec.Command("psql", "-f", file, "isucon5f").Output()
	checkErr(err)
}

var httpport = flag.Int("port", 0, "port to listen")
var logpath = flag.String("logpath", "/tmp/app.log", "log path")

func main() {
	kenCache = map[string]Data{}
	ken2Cache = map[string]Data{}
	surnameCache = map[string]Data{}
	givennameCache = map[string]Data{}

	f, err := os.OpenFile(*logpath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)

	flag.Parse()
	host := os.Getenv("ISUCON5_DB_HOST")
	if host == "" {
		host = "localhost"
	}
	portstr := os.Getenv("ISUCON5_DB_PORT")
	if portstr == "" {
		portstr = "5432"
	}
	port, err := strconv.Atoi(portstr)
	if err != nil {
		log.Fatalf("Failed to read DB port number from an environment variable ISUCON5_DB_PORT.\nError: %s", err.Error())
	}
	user := os.Getenv("ISUCON5_DB_USER")
	if user == "" {
		user = "isucon"
	}
	password := os.Getenv("ISUCON5_DB_PASSWORD")
	dbname := os.Getenv("ISUCON5_DB_NAME")
	if dbname == "" {
		dbname = "isucon5f"
	}
	ssecret := os.Getenv("ISUCON5_SESSION_SECRET")
	if ssecret == "" {
		ssecret = "tonymoris"
	}

	db, err = sql.Open("postgres", "host="+host+" port="+strconv.Itoa(port)+" user="+user+" dbname="+dbname+" sslmode=disable password="+password)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}
	defer db.Close()

	store = sessions.NewCookieStore([]byte(ssecret))

	r := mux.NewRouter()

	s := r.Path("/signup").Subrouter()
	s.Methods("GET").HandlerFunc(GetSignUp)
	s.Methods("POST").HandlerFunc(PostSignUp)

	l := r.Path("/login").Subrouter()
	l.Methods("GET").HandlerFunc(GetLogin)
	l.Methods("POST").HandlerFunc(PostLogin)

	r.HandleFunc("/logout", GetLogout).Methods("GET")

	m := r.Path("/modify").Subrouter()
	m.Methods("GET").HandlerFunc(GetModify)
	m.Methods("POST").HandlerFunc(PostModify)

	r.HandleFunc("/data", GetData).Methods("GET")

	r.HandleFunc("/cancel", PostCancel).Methods("POST")

	r.HandleFunc("/user.js", GetUserJs).Methods("GET")

	r.HandleFunc("/initialize", GetInitialize).Methods("GET")

	r.HandleFunc("/", GetIndex)
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("../static")))

	sigchan := make(chan os.Signal)
	signal.Notify(sigchan, syscall.SIGTERM)
	signal.Notify(sigchan, syscall.SIGINT)

	var li net.Listener
	sock := "/dev/shm/server.sock"
	if *httpport == 0 {
		ferr := os.Remove(sock)
		if ferr != nil {
			if !os.IsNotExist(ferr) {
				panic(ferr.Error())
			}
		}
		li, err = net.Listen("unix", sock)
		cerr := os.Chmod(sock, 0666)
		if cerr != nil {
			panic(cerr.Error())
		}
	} else {
		li, err = net.ListenTCP("tcp", &net.TCPAddr{Port: int(*httpport)})
	}
	if err != nil {
		panic(err.Error())
	}
	go func() {
		// func Serve(l net.Listener, handler Handler) error
		log.Fatal(http.Serve(li, r))
	}()

	<-sigchan
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}
