package goframework

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type LogData struct {
	Author   string
	AuthorId uuid.UUID
	ActionAt time.Time
}

type DataList[T interface{}] struct {
	Data  []T
	Total int64
}

type MongoDbRepository[T interface{}] struct {
	collection *mongo.Collection
	dataList   *DataList[T]
}

func NewMongoDbRepository[T interface{}](
	db *mongo.Database,
) IRepository[T] {
	var r T
	coll := db.Collection(strings.ToLower(reflect.TypeOf(r).Name()))
	return &MongoDbRepository[T]{
		collection: coll,
		dataList:   &DataList[T]{},
	}
}

func (r *MongoDbRepository[T]) GetAll(
	ctx context.Context,
	filter map[string]interface{},
	optsFind ...*options.FindOptions) *[]T {

	filterAggregator := make(map[string][]interface{})
	filterAggregator["$and"] = append(filterAggregator["$and"], filter)

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filterAggregator["$and"] = append(filterAggregator["$and"], map[string]interface{}{"$or": bson.A{
			bson.D{{"tenantId", uuid.MustParse(tenantId)}},
			bson.D{{"tenantId", uuid.MustParse("00000000-0000-0000-0000-000000000000")}},
		},
		})
		filterAggregator["$and"] = append(filterAggregator["$and"], bson.D{{"active", true}})
	}

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filterAggregator)
		fmt.Print(bson.Raw(obj), err)
	}

	cur, err := r.collection.Find(getContext(ctx), filterAggregator, optsFind...)
	if err != nil {
		panic(err)
	}
	result := []T{}
	for cur.Next(ctx) {
		var el T
		err = cur.Decode(&el)
		if err != nil {
			panic(err)
		}
		result = append(result, el)
	}

	return &result
}

func (r *MongoDbRepository[T]) GetAllSkipTake(
	ctx context.Context,
	filter map[string]interface{},
	skip int64,
	take int64,
	optsFind ...*options.FindOptions) *DataList[T] {

	result := &DataList[T]{}

	filterAggregator := make(map[string][]interface{})
	filterAggregator["$and"] = append(filterAggregator["$and"], filter)

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filterAggregator["$and"] = append(filterAggregator["$and"], map[string]interface{}{"$or": bson.A{
			bson.D{{"tenantId", uuid.MustParse(tenantId)}},
			bson.D{{"tenantId", uuid.MustParse("00000000-0000-0000-0000-000000000000")}},
			bson.D{{"active", true}},
		},
		})
	}

	opts := make([]*options.FindOptions, 0)

	op := options.Find()
	op.SetSkip(skip)
	op.SetLimit(take)

	opts = append(opts, op)
	opts = append(opts, optsFind...)

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filterAggregator)
		fmt.Print(bson.Raw(obj), err)
	}

	mCtx := getContext(ctx)

	result.Total, _ = r.collection.CountDocuments(mCtx, filterAggregator)
	if result.Total > 0 {

		cur, err := r.collection.Find(mCtx, filterAggregator, opts...)

		if err != nil {
			panic(err)
		}
		for cur.Next(ctx) {
			var el T
			err = cur.Decode(&el)
			if err != nil {
				panic(err)
			}
			result.Data = append(result.Data, el)
		}
	}

	return result
}

func (r *MongoDbRepository[T]) GetFirst(
	ctx context.Context,
	filter map[string]interface{}) *T {
	var el T

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filter["$or"] = bson.A{
			bson.D{{"tenantId", uuid.MustParse(tenantId)}},
			bson.D{{"tenantId", uuid.MustParse("00000000-0000-0000-0000-000000000000")}},
		}
		filter["active"] = true
	}

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filter)
		fmt.Print(bson.Raw(obj), err)
	}

	err := r.collection.FindOne(getContext(ctx), filter).Decode(&el)

	if err == mongo.ErrNoDocuments {
		return nil
	}

	if err != nil {
		panic(err)
	}

	return &el
}

func (r *MongoDbRepository[T]) insertDefaultParam(ctx context.Context, entity *T) (bson.M, error) {
	bsonMap, err := bson.MarshalWithRegistry(mongoRegistry, entity)
	if err != nil {
		return nil, err
	}

	var bsonM bson.M
	err = bson.Unmarshal(bsonMap, &bsonM)
	if err != nil {
		return nil, err
	}
	// helperContext(ctx, bsonM, map[string]string{"createdBy": "X-Author", "updatedBy": "X-Author"})
	if tenantid := getContextHeader(ctx, "X-Tenant-Id"); tenantid != "" {
		bsonM["tenantId"] = uuid.MustParse(tenantid)
	}

	var history = make(map[string]interface{})
	history["actionAt"] = time.Now()
	helperContext(ctx, history, map[string]string{"author": "X-Author", "authorId": "X-Author-Id"})

	bsonM["created"] = history
	bsonM["updated"] = history
	bsonM["active"] = true

	return bsonM, nil
}

func (r *MongoDbRepository[T]) replaceDefaultParam(ctx context.Context, old bson.M, entity *T) (bson.M, error) {
	bsonMap, err := bson.MarshalWithRegistry(mongoRegistry, entity)
	if err != nil {
		return nil, err
	}

	var bsonM bson.M
	err = bson.Unmarshal(bsonMap, &bsonM)
	if err != nil {
		return nil, err
	}

	var history = make(map[string]interface{})
	history["actionAt"] = time.Now()
	helperContext(ctx, history, map[string]string{"author": "X-Author", "authorId": "X-Author-Id"})

	bsonM["tenantId"] = old["tenantId"]
	bsonM["created"] = old["created"]
	bsonM["updated"] = history
	bsonM["active"] = old["active"]

	return bsonM, nil
}

func (r *MongoDbRepository[T]) Insert(
	ctx context.Context,
	entity *T) error {

	opt := options.InsertOne()
	opt.SetBypassDocumentValidation(true)

	bsonM, err := r.insertDefaultParam(ctx, entity)
	if err != nil {
		return err
	}

	_, err = r.collection.InsertOne(ctx, bsonM, opt)
	if err != nil {
		return err
	}

	return nil
}

func (r *MongoDbRepository[T]) InsertAll(
	ctx context.Context,
	entities *[]T) error {

	var uis []interface{}
	for _, ui := range *entities {
		bsonM, err := r.insertDefaultParam(ctx, &ui)
		if err != nil {
			return err
		}

		uis = append(uis, bsonM)
	}
	_, err := r.collection.InsertMany(getContext(ctx), uis)
	if err != nil {
		return err
	}

	return nil
}

func (r *MongoDbRepository[T]) Replace(
	ctx context.Context,
	filter map[string]interface{},
	entity *T) error {

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filter["tenantId"] = uuid.MustParse(tenantId)
	}

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filter)
		fmt.Print(bson.Raw(obj), err)
	}

	var el bson.M
	err := r.collection.FindOne(getContext(ctx), filter).Decode(&el)

	if err == mongo.ErrNoDocuments {
		return r.Insert(ctx, entity)
	}

	bsonM, err := r.replaceDefaultParam(ctx, el, entity)
	if err != nil {
		return err
	}

	_, err = r.collection.ReplaceOne(getContext(ctx), filter, bsonM, options.Replace().SetUpsert(true))
	if err != nil {
		return err
	}

	return nil
}

func (r *MongoDbRepository[T]) Update(
	ctx context.Context,
	filter map[string]interface{},
	fields interface{}) error {

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filter["tenantId"] = uuid.MustParse(tenantId)
	}

	setBson := structToBson(fields)
	var history = make(map[string]interface{})
	history["actionAt"] = time.Now()
	helperContext(ctx, history, map[string]string{"author": "X-Author", "authorId": "X-Author-Id"})
	setBson["updated"] = history

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filter)
		fmt.Print(bson.Raw(obj), err)
	}

	re, err := r.collection.UpdateOne(getContext(ctx), filter, map[string]interface{}{"$set": setBson})

	if err != nil {
		return err
	}

	if re.MatchedCount == 0 {
		return fmt.Errorf("MatchedCountZero")
	}

	return nil
}

func (r *MongoDbRepository[T]) Delete(
	ctx context.Context,
	filter map[string]interface{}) error {

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filter["$or"] = bson.A{
			bson.D{{"tenantId", uuid.MustParse(tenantId)}},
		}
	}

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filter)
		fmt.Print(bson.Raw(obj), err)
	}

	setBson := bson.M{"active": false}
	re, err := r.collection.UpdateOne(getContext(ctx), filter, map[string]interface{}{"$set": setBson})

	if err != nil {
		return err
	}

	if re.MatchedCount == 0 {
		return fmt.Errorf("MatchedCountZero")
	}

	return nil
}

func (r *MongoDbRepository[T]) DeleteForce(
	ctx context.Context,
	filter map[string]interface{}) error {

	if tenantId := getContextHeader(ctx, "X-Tenant-Id"); tenantId != "" {
		filter["$or"] = bson.A{
			bson.D{{"tenantId", uuid.MustParse(tenantId)}},
		}
	}

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filter)
		fmt.Print(bson.Raw(obj), err)
	}

	_, err := r.collection.DeleteOne(getContext(ctx), filter)

	if err == mongo.ErrNoDocuments {
		return nil
	}

	if err != nil {
		return err
	}

	return nil
}

func (r *MongoDbRepository[T]) Aggregate(ctx context.Context, pipeline []interface{}) (*mongo.Cursor, error) {
	filter := bson.A{
		bson.D{
			{"$match",
				bson.D{
					{"$or",
						bson.A{
							bson.D{{"tenantId", uuid.MustParse("00000000-0000-0000-0000-000000000000")}},
							bson.D{{"tenantId", uuid.MustParse(getContextHeader(ctx, "X-Tenant-Id"))}},
						},
					},
					{"active", true},
				},
			},
		},
	}

	filter = append(filter, pipeline...)

	if os.Getenv("env") == "local" {
		_, obj, err := bson.MarshalValue(filter)
		fmt.Print(bson.Raw(obj), err)
	}

	return r.collection.Aggregate(ctx, filter)
}
