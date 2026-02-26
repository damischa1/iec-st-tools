TYPE
    TestStruct : STRUCT
        Value   : INT := 0;
        IsValid : BOOL := FALSE;
        Name    : STRING := '';
    END_STRUCT;

    TestEnum : (
        stIdle   := 0,
        stactive := 1,
        stError  := 2
    );
END_TYPE
