package samples.lang;

public class Order {
    private String id;
    private int amount;
    private Status status;

    public Order(String id, int amount, Status status) {
        this.id = id;
        this.amount = amount;
        this.status = status;
    }

    public String getId() {
        return id;
    }

    public int getAmount() {
        return amount;
    }

    public Status getStatus() {
        return status;
    }
}
